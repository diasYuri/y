package agent

import (
	"errors"
	"time"

	"github.com/diasYuri/y/pkg/ai"
	ycontext "github.com/diasYuri/y/pkg/context"
	"github.com/diasYuri/y/pkg/policy"
	"github.com/diasYuri/y/pkg/providers"
)

// AgentSnapshot captures the in-memory state of an [Agent] required to
// rehydrate it later. It is a value type so callers can JSON-encode it,
// store it, and feed it back into [RestoreAgent] or [Agent.Restore].
//
// Snapshots intentionally exclude things that cannot be serialized
// portably: the provider, the tool registry, hooks, event sinks, the logger,
// the usage observer, and any in-flight contexts. Callers are expected to
// provide those again when restoring.
//
// # Recoverable error preservation
//
// When the agent is in [StateFailed] with a recoverable error, Snapshot
// captures the error's message in [AgentSnapshot.RecoverableErrMsg] so that
// Restore can rebuild a sentinel marker and let [Agent.Continue] /
// [Agent.Recover] keep working after a round-trip. The original error type
// is lost across the round-trip; the sentinel is a plain
// errors.New("restored: <msg>") that satisfies isRecoverable through
// [ErrRetry] semantics enforced by Restore.
//
// # StreamDefaults
//
// Snapshot serializes only the JSON-friendly subset of
// [providers.StreamOptions]: Temperature, MaxTokens, Timeout, MaxRetries,
// MaxRetryDelay, CacheRetention, Reasoning, and ThinkingBudgets.
// Non-portable fields (Transport, Headers, APIKey, Extras, Metadata,
// SessionID) are intentionally dropped — callers must reapply them via
// options when restoring.
type AgentSnapshot struct {
	// SchemaVersion identifies the serialisation contract. Zero is accepted as
	// version one for backwards compatibility with pre-versioned snapshots.
	SchemaVersion int `json:"schema_version,omitempty"`

	// Version is the agent-local checkpoint version. Distributed stores add
	// their own optimistic version around this value.
	Version uint64 `json:"version,omitempty"`

	// RunID identifies the execution that produced this state.
	RunID string `json:"run_id,omitempty"`

	// RunNonce identifies the logical execution generation used for tool
	// idempotency. It is retained while a run is recoverable and cleared after
	// a terminal run settles.
	RunNonce string `json:"run_nonce,omitempty"`

	// ContextRequest identifies the context scopes used by the run.
	ContextRequest ycontext.Request `json:"context_request,omitempty"`

	// PolicyIdentity is the non-secret authorization identity used by tools.
	PolicyIdentity         policy.Identity `json:"policy_identity,omitempty"`
	PolicyVersion          string          `json:"policy_version,omitempty"`
	AuthorizationExpiresAt time.Time       `json:"authorization_expires_at,omitempty"`
	IdempotencyKey         string          `json:"idempotency_key,omitempty"`

	// Transcript is a deep copy of the agent's conversation transcript.
	Transcript []ai.Message `json:"transcript,omitempty"`

	// Model is the resolved model (if any) used by the most recent run.
	Model ai.Model `json:"model"`

	// SystemPrompt mirrors the value passed to [WithSystemPrompt].
	SystemPrompt string `json:"system_prompt,omitempty"`

	// WorkspaceRoot mirrors [WithWorkspaceRoot].
	WorkspaceRoot string `json:"workspace_root,omitempty"`

	// MaxTurns mirrors [WithMaxTurns].
	MaxTurns int `json:"max_turns,omitempty"`

	// SessionID mirrors [WithSessionID].
	SessionID string `json:"session_id,omitempty"`

	// ThinkingBudgets mirrors [WithThinkingBudgets].
	ThinkingBudgets map[ai.ThinkingLevel]int64 `json:"thinking_budgets,omitempty"`

	// State is the lifecycle state captured at snapshot time. The most
	// useful value is [StateCompleted] (resume by calling Run again) or
	// [StateFailed] (resume by calling Recover).
	State State `json:"state,omitempty"`

	// FollowUpQueue contains messages queued for the next run via
	// [Agent.FollowUp].
	FollowUpQueue []ai.Message `json:"follow_up_queue,omitempty"`

	// PendingSteering contains messages queued for steering that have
	// not yet been injected.
	PendingSteering []ai.Message `json:"pending_steering,omitempty"`

	// PendingApproval is the durable tool continuation awaiting a remote
	// approval resolution.
	PendingApproval *PendingApproval `json:"pending_approval,omitempty"`

	// RecoverableErrMsg, when non-empty, indicates the agent failed with a
	// recoverable error whose message is preserved here. Restore uses this
	// to rebuild a sentinel error so [Agent.Continue] and [Agent.Recover]
	// behave correctly. The original concrete error type is not preserved.
	RecoverableErrMsg string `json:"recoverable_err_msg,omitempty"`

	// ToolMode mirrors [WithToolExecutionMode].
	ToolMode ToolExecutionMode `json:"tool_mode,omitempty"`

	// ToolConcurrency mirrors [WithToolConcurrency].
	ToolConcurrency int `json:"tool_concurrency,omitempty"`

	// ToolTimeout mirrors [WithToolTimeout].
	ToolTimeout time.Duration `json:"tool_timeout,omitempty"`

	// MaxRetries mirrors [WithMaxRetries].
	MaxRetries int `json:"max_retries,omitempty"`

	// MaxRetryDelay mirrors [WithMaxRetryDelay].
	MaxRetryDelay time.Duration `json:"max_retry_delay,omitempty"`

	// CompactionEnabled mirrors [WithCompaction].
	CompactionEnabled bool `json:"compaction_enabled,omitempty"`

	// StreamDefaults captures the JSON-friendly subset of the
	// [providers.StreamOptions] passed to [WithStreamDefaults]. The fields
	// Transport, Headers, APIKey, SessionID, Extras and Metadata are
	// intentionally dropped (they are not safe to round-trip).
	StreamDefaults StreamOptionsSnapshot `json:"stream_defaults,omitempty"`
}

// StreamOptionsSnapshot is the JSON-friendly subset of
// [providers.StreamOptions] preserved across a snapshot round-trip.
type StreamOptionsSnapshot struct {
	Temperature     *float64                   `json:"temperature,omitempty"`
	MaxTokens       int64                      `json:"max_tokens,omitempty"`
	CacheRetention  ai.CacheRetention          `json:"cache_retention,omitempty"`
	Timeout         time.Duration              `json:"timeout,omitempty"`
	MaxRetries      int                        `json:"max_retries,omitempty"`
	MaxRetryDelay   time.Duration              `json:"max_retry_delay,omitempty"`
	Reasoning       ai.ThinkingLevel           `json:"reasoning,omitempty"`
	ThinkingBudgets map[ai.ThinkingLevel]int64 `json:"thinking_budgets,omitempty"`
	ResponseFormat  *ai.ResponseFormat         `json:"response_format,omitempty"`
}

func streamOptionsToSnapshot(opts providers.StreamOptions) StreamOptionsSnapshot {
	out := StreamOptionsSnapshot{
		Temperature:    opts.Temperature,
		MaxTokens:      opts.MaxTokens,
		CacheRetention: opts.CacheRetention,
		Timeout:        opts.Timeout,
		MaxRetries:     opts.MaxRetries,
		MaxRetryDelay:  opts.MaxRetryDelay,
		Reasoning:      opts.Reasoning,
		ResponseFormat: cloneResponseFormat(opts.ResponseFormat),
	}
	if len(opts.ThinkingBudgets) > 0 {
		out.ThinkingBudgets = make(map[ai.ThinkingLevel]int64, len(opts.ThinkingBudgets))
		for k, v := range opts.ThinkingBudgets {
			out.ThinkingBudgets[k] = v
		}
	}
	return out
}

func streamOptionsFromSnapshot(s StreamOptionsSnapshot) providers.StreamOptions {
	out := providers.StreamOptions{
		Temperature:    s.Temperature,
		MaxTokens:      s.MaxTokens,
		CacheRetention: s.CacheRetention,
		Timeout:        s.Timeout,
		MaxRetries:     s.MaxRetries,
		MaxRetryDelay:  s.MaxRetryDelay,
		Reasoning:      s.Reasoning,
		ResponseFormat: cloneResponseFormat(s.ResponseFormat),
	}
	if len(s.ThinkingBudgets) > 0 {
		out.ThinkingBudgets = make(map[ai.ThinkingLevel]int64, len(s.ThinkingBudgets))
		for k, v := range s.ThinkingBudgets {
			out.ThinkingBudgets[k] = v
		}
	}
	return out
}

func cloneResponseFormat(format *ai.ResponseFormat) *ai.ResponseFormat {
	if format == nil {
		return nil
	}
	cloned := *format
	cloned.Schema = append([]byte(nil), format.Schema...)
	return &cloned
}

// Validate checks whether a snapshot can be safely rehydrated by this
// version of the SDK. Version zero is the legacy, pre-versioned format.
func (s AgentSnapshot) Validate() error {
	if s.SchemaVersion < 0 || s.SchemaVersion > CurrentSnapshotSchemaVersion {
		return errors.New("agent: unsupported snapshot schema version")
	}
	if s.PendingApproval != nil && s.PendingApproval.ToolCall.Name == "" {
		return errors.New("agent: pending approval has no tool name")
	}
	if s.MaxTurns < 0 {
		return errors.New("agent: snapshot contains a negative MaxTurns")
	}
	if s.ToolConcurrency < 0 {
		return errors.New("agent: snapshot contains a negative ToolConcurrency")
	}
	if s.MaxRetries < 0 {
		return errors.New("agent: snapshot contains a negative MaxRetries")
	}
	if s.ToolTimeout < 0 {
		return errors.New("agent: snapshot contains a negative ToolTimeout")
	}
	if s.MaxRetryDelay < 0 {
		return errors.New("agent: snapshot contains a negative MaxRetryDelay")
	}
	return nil
}

// Migrate upgrades legacy agent snapshots in memory. Version zero is the
// pre-versioned format; newer unknown versions are rejected explicitly.
func (s AgentSnapshot) Migrate() (AgentSnapshot, error) {
	if s.SchemaVersion < 0 || s.SchemaVersion > CurrentSnapshotSchemaVersion {
		return AgentSnapshot{}, errors.New("agent: unsupported snapshot schema version")
	}
	if s.SchemaVersion == 0 {
		s.SchemaVersion = CurrentSnapshotSchemaVersion
	}
	for i := range s.Transcript {
		message, err := ai.MigrateMessage(s.Transcript[i])
		if err != nil {
			return AgentSnapshot{}, err
		}
		s.Transcript[i] = message
	}
	return s, nil
}

// Snapshot returns a deep copy of the agent's serialisable state. Snapshot
// is safe to call from any goroutine, including while the agent is running;
// the returned snapshot reflects the moment Snapshot was called.
func (a *Agent) Snapshot() AgentSnapshot {
	a.mu.Lock()
	transcript := cloneMessages(a.transcript)
	model := a.model
	systemPrompt := a.systemPrompt
	workspaceRoot := a.workspaceRoot
	maxTurns := a.maxTurns
	sessionID := a.sessionID
	state := a.state
	toolMode := a.toolMode
	toolConcurrency := a.toolConcurrency
	toolTimeout := a.toolTimeout
	maxRetries := a.maxRetries
	maxRetryDelay := a.maxRetryDelay
	compactionEnabled := a.compactionEnabled
	streamDefaults := a.streamDefaults
	contextRequest := a.contextRequest
	policyIdentity := a.policyIdentity
	policyVersion := a.policyVersion
	authorizationExpiresAt := a.authorizationExpiresAt
	idempotencyKey := a.idempotencyKey
	runID := a.runID
	runNonce := a.runNonce
	transcriptVersion := a.transcriptVersion

	var thinking map[ai.ThinkingLevel]int64
	if len(a.thinkingBudgets) > 0 {
		thinking = make(map[ai.ThinkingLevel]int64, len(a.thinkingBudgets))
		for k, v := range a.thinkingBudgets {
			thinking[k] = v
		}
	}

	var recoverableMsg string
	if a.recoverableErr != nil {
		recoverableMsg = a.recoverableErr.Error()
	}
	pendingApproval := clonePendingApproval(a.pendingApproval)
	a.mu.Unlock()

	a.followUpMu.Lock()
	followUp := cloneMessages(a.followUpQueue)
	a.followUpMu.Unlock()

	a.steeringMu.Lock()
	steering := cloneMessages(a.steeringQueue)
	a.steeringMu.Unlock()

	return AgentSnapshot{
		SchemaVersion:          CurrentSnapshotSchemaVersion,
		Version:                transcriptVersion,
		RunID:                  runID,
		RunNonce:               runNonce,
		ContextRequest:         contextRequest,
		PolicyIdentity:         clonePolicyIdentity(policyIdentity),
		PolicyVersion:          policyVersion,
		AuthorizationExpiresAt: authorizationExpiresAt,
		IdempotencyKey:         idempotencyKey,
		Transcript:             transcript,
		Model:                  sanitizeModel(model),
		SystemPrompt:           systemPrompt,
		WorkspaceRoot:          workspaceRoot,
		MaxTurns:               maxTurns,
		SessionID:              sessionID,
		ThinkingBudgets:        thinking,
		State:                  state,
		FollowUpQueue:          followUp,
		PendingSteering:        steering,
		PendingApproval:        pendingApproval,
		RecoverableErrMsg:      recoverableMsg,
		ToolMode:               toolMode,
		ToolConcurrency:        toolConcurrency,
		ToolTimeout:            toolTimeout,
		MaxRetries:             maxRetries,
		MaxRetryDelay:          maxRetryDelay,
		CompactionEnabled:      compactionEnabled,
		StreamDefaults:         streamOptionsToSnapshot(streamDefaults),
	}
}

// Restore replaces the agent's serialisable state with snap. In-flight runs
// are not aborted; callers should ensure no run is active before calling
// Restore. Restore overwrites every supported field unconditionally — pass
// a fully-populated snapshot, not a sparse delta. Returns an error only when
// snap is structurally invalid.
//
// The provider, tool registry, hooks, sinks, logger, and usage observer are
// not touched by Restore. The recoverable-error sentinel is rebuilt from
// [AgentSnapshot.RecoverableErrMsg]: the original concrete error type is
// lost, but [Agent.Continue] and [Agent.Recover] can still resume.
func (a *Agent) Restore(snap AgentSnapshot) error {
	var err error
	snap, err = snap.Migrate()
	if err != nil {
		return err
	}
	if err := snap.Validate(); err != nil {
		return err
	}

	a.mu.Lock()
	a.transcript = cloneMessages(snap.Transcript)
	a.transcriptVersion = snap.Version
	if a.transcriptVersion == 0 && len(a.transcript) > 0 {
		a.transcriptVersion = 1
	}
	a.runID = snap.RunID
	a.runNonce = snap.RunNonce
	a.contextRequest = snap.ContextRequest
	a.policyIdentity = clonePolicyIdentity(snap.PolicyIdentity)
	a.policyVersion = snap.PolicyVersion
	a.authorizationExpiresAt = snap.AuthorizationExpiresAt
	a.idempotencyKey = snap.IdempotencyKey
	a.model = sanitizeModel(snap.Model)
	a.systemPrompt = snap.SystemPrompt
	a.workspaceRoot = snap.WorkspaceRoot
	if snap.MaxTurns > 0 {
		a.maxTurns = snap.MaxTurns
	} else {
		a.maxTurns = defaultMaxTurns
	}
	a.sessionID = snap.SessionID
	if len(snap.ThinkingBudgets) > 0 {
		a.thinkingBudgets = make(map[ai.ThinkingLevel]int64, len(snap.ThinkingBudgets))
		for k, v := range snap.ThinkingBudgets {
			a.thinkingBudgets[k] = v
		}
	} else {
		a.thinkingBudgets = nil
	}
	if snap.State == "" {
		a.state = StateIdle
	} else {
		a.state = snap.State
	}

	if snap.ToolMode != "" {
		a.toolMode = snap.ToolMode
	} else {
		a.toolMode = ToolExecutionParallel
	}
	a.toolConcurrency = snap.ToolConcurrency
	a.toolTimeout = snap.ToolTimeout
	a.maxRetries = snap.MaxRetries
	a.maxRetryDelay = snap.MaxRetryDelay
	a.compactionEnabled = snap.CompactionEnabled
	a.streamDefaults = streamOptionsFromSnapshot(snap.StreamDefaults)
	a.pendingApproval = clonePendingApproval(snap.PendingApproval)
	a.approvalResolution = nil

	if snap.RecoverableErrMsg != "" {
		a.recoverableErr = errors.New("restored: " + snap.RecoverableErrMsg)
	} else {
		a.recoverableErr = nil
	}
	a.mu.Unlock()

	a.followUpMu.Lock()
	a.followUpQueue = cloneMessages(snap.FollowUpQueue)
	a.followUpMu.Unlock()

	a.steeringMu.Lock()
	a.steeringQueue = cloneMessages(snap.PendingSteering)
	a.steeringMu.Unlock()

	return nil
}

func clonePolicyIdentity(identity policy.Identity) policy.Identity {
	identity.Capabilities = append([]string(nil), identity.Capabilities...)
	return identity
}

func cloneApprovalResolution(resolution *policy.ApprovalResolution) *policy.ApprovalResolution {
	if resolution == nil {
		return nil
	}
	copy := *resolution
	return &copy
}

func clonePendingApproval(pending *PendingApproval) *PendingApproval {
	if pending == nil {
		return nil
	}
	copy := *pending
	copy.ToolCall.Arguments = append([]byte(nil), pending.ToolCall.Arguments...)
	return &copy
}

func sanitizeModel(model ai.Model) ai.Model {
	model.Headers = nil
	// Provider metadata can contain opaque authentication material. It is
	// re-resolved by the provider on the next request instead of being carried
	// across a worker boundary.
	model.Metadata = nil
	return model
}

// RestoreAgent constructs a new agent and applies snap to it. Any options
// supplied on top of the snapshot win (because Option callbacks run after
// Restore inside this helper).
func RestoreAgent(provider Provider, registry ToolRegistry, snap AgentSnapshot, opts ...Option) (*Agent, error) {
	a := New(provider, registry)
	if err := a.Restore(snap); err != nil {
		return nil, err
	}
	for _, opt := range opts {
		if opt != nil {
			opt(a)
		}
	}
	return a, nil
}
