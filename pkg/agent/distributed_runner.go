package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/policy"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/providers/auth"
	"github.com/yuri/y/pkg/telemetry"
	"github.com/yuri/y/pkg/tools"
)

type AgentRunner struct {
	provider         Provider
	providerResolver ProviderResolver
	registry         ToolRegistry
	modelCatalog     ModelCatalog
	credentials      CredentialResolver
	stateStore       StateStore
	transcriptStore  TranscriptStore
	eventStore       EventStore
	operationStore   OperationStore
	control          RunControl
	options          []Option
	pollEvery        time.Duration
	owner            string
	tracer           telemetry.Tracer
	runLocksMu       sync.Mutex
	runLocks         map[string]*sync.Mutex
}

// eventBackedToolRegistry reconciles a tool call from the durable event log
// before invoking its handler. The operation key is stable across workers
// because Agent persists the run nonce in its checkpoint. EventStore is the
// transactional handoff available to the transport-neutral runner; without a
// durable event store, execution remains explicitly ephemeral.
type eventBackedToolRegistry struct {
	base       ToolRegistry
	events     EventStore
	operations OperationStore
}

func (r *eventBackedToolRegistry) List() []tools.ToolDescriptor {
	if r.base == nil {
		return nil
	}
	return r.base.List()
}

func (r *eventBackedToolRegistry) GetExecutionMode(name string) tools.ExecutionMode {
	if r.base == nil {
		return tools.ExecutionParallel
	}
	return r.base.GetExecutionMode(name)
}

func (r *eventBackedToolRegistry) Handle(ctx context.Context, request tools.ToolRequest) (tools.ToolResponse, error) {
	if request.IdempotencyKey != "" && request.RunID != "" && r.events != nil {
		events, err := r.events.Read(ctx, request.RunID, 0)
		if err != nil {
			return tools.ToolResponse{}, err
		}
		for _, event := range events {
			if event.Type != EventToolCompleted || event.IdempotencyKey != request.IdempotencyKey {
				continue
			}
			var payload struct {
				ToolResult ai.ToolResult `json:"tool_result"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return tools.ToolResponse{}, fmt.Errorf("decode durable tool result: %w", err)
			}
			if payload.ToolResult.ToolCallID != "" && request.ID != "" && payload.ToolResult.ToolCallID != request.ID {
				continue
			}
			if payload.ToolResult.ToolName != "" && request.Name != "" && payload.ToolResult.ToolName != request.Name {
				continue
			}
			return toolResponseFromAIResult(payload.ToolResult), nil
		}
		if r.operations == nil {
			return tools.ToolResponse{}, ErrOperationStoreUnavailable
		}
		requested := OperationRecord{
			SchemaVersion:  CurrentOperationSchemaVersion,
			IdempotencyKey: request.IdempotencyKey,
			RunID:          request.RunID,
			TurnID:         request.TurnID,
			ToolCallID:     request.ID,
			ToolName:       request.Name,
			ArgumentsHash:  operationArgumentsHash(request.Arguments),
		}
		existing, acquired, err := r.operations.BeginOperation(ctx, requested)
		if err != nil {
			return tools.ToolResponse{}, err
		}
		if !acquired {
			if existing.State == OperationPending {
				if reconciler, ok := r.operations.(OperationReconciler); ok {
					response, reconcileErr, found := reconciler.ReconcileOperation(ctx, existing)
					if found {
						if completeErr := r.operations.CompleteOperation(ctx, existing.IdempotencyKey, response, reconcileErr); completeErr != nil {
							return response, fmt.Errorf("complete reconciled operation: %w", completeErr)
						}
						return response, reconcileErr
					}
				}
			}
			if existing.Response != nil {
				if existing.Error != nil {
					return cloneToolResponse(*existing.Response), operationError(existing.Error)
				}
				return cloneToolResponse(*existing.Response), nil
			}
			if existing.Error != nil {
				return tools.ToolResponse{}, operationError(existing.Error)
			}
			return tools.ToolResponse{}, ErrOperationInDoubt
		}
	}
	if r.base == nil {
		return tools.ToolResponse{}, errors.New("agent: tool registry is nil")
	}
	response, err := r.base.Handle(ctx, request)
	if request.IdempotencyKey != "" && request.RunID != "" && r.operations != nil {
		var approvalErr *tools.ApprovalError
		if errors.As(err, &approvalErr) {
			if releaser, ok := r.operations.(operationReleaser); ok {
				_ = releaser.ReleaseOperation(ctx, request.IdempotencyKey)
			}
			return response, err
		}
		if completeErr := r.operations.CompleteOperation(ctx, request.IdempotencyKey, response, err); completeErr != nil {
			return response, fmt.Errorf("complete durable operation: %w", completeErr)
		}
	}
	return response, err
}

func operationError(eventErr *EventError) error {
	if eventErr == nil {
		return nil
	}
	return errors.New(eventErr.Message)
}

func operationArgumentsHash(arguments []byte) string {
	sum := sha256.Sum256(arguments)
	return hex.EncodeToString(sum[:])
}

func toolResponseFromAIResult(result ai.ToolResult) tools.ToolResponse {
	response := tools.ToolResponse{
		IsError:  result.IsError,
		Details:  append([]byte(nil), result.Details...),
		Metadata: append([]byte(nil), result.Metadata...),
	}
	for _, log := range result.Logs {
		response.Logs = append(response.Logs, tools.LogEntry{Timestamp: log.Timestamp, Level: log.Level, Message: log.Message, Details: append([]byte(nil), log.Details...)})
	}
	for _, block := range result.Content {
		contentType := tools.ContentType(block.Type)
		if contentType != tools.ContentText && contentType != tools.ContentImage {
			continue
		}
		response.Content = append(response.Content, tools.ContentBlock{
			Type:             contentType,
			Text:             block.Text,
			ImageData:        append([]byte(nil), block.ImageData...),
			ImageMIMEType:    block.ImageMIMEType,
			Details:          append([]byte(nil), block.Details...),
			ProviderMetadata: append([]byte(nil), block.ProviderMetadata...),
		})
	}
	return response
}

// ModelCatalog resolves a request-selected model without making the runner
// depend on a concrete provider catalog implementation.
type ModelCatalog interface {
	Lookup(context.Context, ai.ProviderID, string) (ai.Model, error)
}

// ProviderResolver selects a concrete provider for a request-scoped provider
// or model. It can be backed by a catalog, tenant configuration, or a remote
// provider service.
type ProviderResolver interface {
	ResolveProvider(context.Context, ai.ProviderID) (providers.Provider, error)
}

type ProviderResolverFunc func(context.Context, ai.ProviderID) (providers.Provider, error)

func (f ProviderResolverFunc) ResolveProvider(ctx context.Context, id ai.ProviderID) (providers.Provider, error) {
	return f(ctx, id)
}

// CredentialResolver supplies a secret for one request. Implementations must
// keep credential values out of snapshots and event payloads.
type CredentialResolver interface {
	ResolveAPIKey(context.Context, auth.ResolveRequest) (string, error)
}

type RunnerOption func(*AgentRunner)

func WithRunnerStateStore(store StateStore) RunnerOption {
	return func(r *AgentRunner) { r.stateStore = store }
}
func WithRunnerEventStore(store EventStore) RunnerOption {
	return func(r *AgentRunner) { r.eventStore = store }
}
func WithRunnerControl(control RunControl) RunnerOption {
	return func(r *AgentRunner) { r.control = control }
}
func WithRunnerModelCatalog(catalog ModelCatalog) RunnerOption {
	return func(r *AgentRunner) { r.modelCatalog = catalog }
}
func WithRunnerCredentialResolver(resolver CredentialResolver) RunnerOption {
	return func(r *AgentRunner) { r.credentials = resolver }
}
func WithRunnerProviderResolver(resolver ProviderResolver) RunnerOption {
	return func(r *AgentRunner) { r.providerResolver = resolver }
}
func WithRunnerOperationStore(store OperationStore) RunnerOption {
	return func(r *AgentRunner) { r.operationStore = store }
}
func WithRunnerTranscriptStore(store TranscriptStore) RunnerOption {
	return func(r *AgentRunner) { r.transcriptStore = store }
}
func WithRunnerAgentOptions(options ...Option) RunnerOption {
	return func(r *AgentRunner) { r.options = append(r.options, options...) }
}
func WithRunnerPollInterval(interval time.Duration) RunnerOption {
	return func(r *AgentRunner) {
		if interval > 0 {
			r.pollEvery = interval
		}
	}
}
func WithRunnerOwner(owner string) RunnerOption { return func(r *AgentRunner) { r.owner = owner } }

// WithRunnerTracer records durable state/load/save/checkpoint spans around a
// stateless AgentRunner. It complements WithTracer, which instruments the
// short-lived Agent execution itself.
func WithRunnerTracer(tracer telemetry.Tracer) RunnerOption {
	return func(r *AgentRunner) { r.tracer = tracer }
}

func NewAgentRunner(provider Provider, registry ToolRegistry, options ...RunnerOption) *AgentRunner {
	r := &AgentRunner{
		provider:   provider,
		registry:   registry,
		stateStore: NoopStore{},
		eventStore: NoopStore{},
		control:    NoopStore{},
		pollEvery:  100 * time.Millisecond,
		owner:      newID(),
		runLocks:   make(map[string]*sync.Mutex),
	}
	for _, option := range options {
		if option != nil {
			option(r)
		}
	}
	if r.stateStore == nil {
		r.stateStore = NoopStore{}
	}
	if r.eventStore == nil {
		r.eventStore = NoopStore{}
	}
	if _, ephemeralEvents := r.eventStore.(NoopStore); ephemeralEvents {
		if store, ok := r.stateStore.(EventStore); ok {
			r.eventStore = store
		}
	}
	if r.operationStore == nil {
		if store, ok := r.eventStore.(OperationStore); ok {
			r.operationStore = store
		} else if _, ephemeralEvents := r.eventStore.(NoopStore); ephemeralEvents {
			r.operationStore = NoopStore{}
		}
	}
	if r.transcriptStore == nil {
		if store, ok := r.stateStore.(TranscriptStore); ok {
			r.transcriptStore = store
		}
	}
	if r.control == nil {
		r.control = NoopStore{}
	}
	if r.pollEvery <= 0 {
		r.pollEvery = 100 * time.Millisecond
	}
	if r.owner == "" {
		r.owner = newID()
	}
	if r.runLocks == nil {
		r.runLocks = make(map[string]*sync.Mutex)
	}
	return r
}

func (r *AgentRunner) loadState(ctx context.Context, runID string) (Snapshot, error) {
	ctx, span := r.startRunnerSpan(ctx, "state.load", runID)
	defer span.End()
	state, err := r.stateStore.Load(ctx, runID)
	if err != nil {
		span.RecordError(err)
	} else {
		span.SetStatus(telemetry.StatusOK, "")
	}
	return state, err
}

func (r *AgentRunner) saveState(ctx context.Context, runID string, expectedVersion uint64, state Snapshot) error {
	ctx, span := r.startRunnerSpan(ctx, "state.save", runID, telemetry.Attribute{Key: "state.version", Value: state.Version})
	defer span.End()
	err := r.stateStore.Save(ctx, runID, expectedVersion, state)
	if err != nil {
		span.RecordError(err)
	} else {
		span.SetStatus(telemetry.StatusOK, "")
	}
	return err
}

func (r *AgentRunner) saveTranscript(ctx context.Context, runID string, expectedVersion uint64, messages []ai.Message) error {
	if r.transcriptStore == nil {
		return nil
	}
	ctx, span := r.startRunnerSpan(ctx, "transcript.save", runID, telemetry.Attribute{Key: "state.version", Value: expectedVersion})
	defer span.End()
	err := r.transcriptStore.SaveTranscript(ctx, runID, expectedVersion, messages)
	if err != nil {
		span.RecordError(err)
	} else {
		span.SetStatus(telemetry.StatusOK, "")
	}
	return err
}

func (r *AgentRunner) startRunnerSpan(ctx context.Context, name, runID string, attributes ...telemetry.Attribute) (context.Context, telemetry.Span) {
	attributes = append([]telemetry.Attribute{{Key: "run.id", Value: runID}}, attributes...)
	if r.tracer == nil {
		return telemetry.NoopTracer{}.Start(ctx, name, attributes...)
	}
	return r.tracer.Start(ctx, name, attributes...)
}

// RunRequest is serialisable except for no process-local dependencies. State
// may be supplied directly or loaded by RunID from the configured StateStore.
type RunRequest struct {
	RunID                  string                     `json:"run_id"`
	SessionID              string                     `json:"session_id,omitempty"`
	TenantID               string                     `json:"tenant_id,omitempty"`
	WorkspaceID            string                     `json:"workspace_id,omitempty"`
	ProjectID              string                     `json:"project_id,omitempty"`
	Identity               policy.Identity            `json:"identity,omitempty"`
	PolicyVersion          string                     `json:"policy_version,omitempty"`
	AuthorizationExpiresAt time.Time                  `json:"authorization_expires_at,omitempty"`
	IdempotencyKey         string                     `json:"idempotency_key,omitempty"`
	RequestID              string                     `json:"request_id,omitempty"`
	ModelID                string                     `json:"model_id,omitempty"`
	ProviderID             ai.ProviderID              `json:"provider_id,omitempty"`
	Model                  ai.Model                   `json:"model,omitempty"`
	SystemPrompt           string                     `json:"system_prompt,omitempty"`
	WorkspaceRoot          string                     `json:"workspace_root,omitempty"`
	Approval               *policy.ApprovalResolution `json:"approval,omitempty"`
	APIKey                 string                     `json:"-"`
	Prompt                 string                     `json:"prompt,omitempty"`
	Messages               []ai.Message               `json:"messages,omitempty"`
	InitialState           *Snapshot                  `json:"initial_state,omitempty"`
}

// RunResponse contains the durable result and every event emitted during this
// invocation. Events are still written to EventStore even when the caller
// chooses to discard this in-memory copy.
type RunResponse struct {
	RunID    string          `json:"run_id"`
	Snapshot Snapshot        `json:"snapshot"`
	Result   RunResult       `json:"result"`
	Events   []EventEnvelope `json:"events,omitempty"`
}
