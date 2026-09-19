package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/tools"
)

const (
	CurrentSnapshotSchemaVersion  = 1
	CurrentEventSchemaVersion     = 1
	CurrentOperationSchemaVersion = 1
)

var (
	ErrStateNotFound             = errors.New("agent: state not found")
	ErrVersionConflict           = errors.New("agent: state version conflict")
	ErrEventOutOfOrder           = errors.New("agent: event sequence is not monotonic")
	ErrDuplicateEvent            = errors.New("agent: duplicate event idempotency key")
	ErrIdempotencyConflict       = errors.New("agent: idempotency key conflicts with a different operation")
	ErrOperationInDoubt          = errors.New("agent: operation outcome is in doubt")
	ErrOperationNotFound         = errors.New("agent: operation not found")
	ErrOperationStoreUnavailable = errors.New("agent: durable operation store is required")
	ErrApprovalPending           = errors.New("agent: approval is pending")
	ErrTranscriptNotFound        = errors.New("agent: transcript not found")
	ErrRunInProgress             = errors.New("agent: run is already in progress")
	ErrInvalidSnapshot           = errors.New("agent: invalid snapshot")
	ErrInvalidEvent              = errors.New("agent: invalid event")
	ErrLeaseHeld                 = errors.New("agent: run lease is held")
	ErrLeaseNotFound             = errors.New("agent: run lease not found")
)

// RunStatus is the durable status of a distributed execution.
type RunStatus string

const (
	RunPending         RunStatus = "pending"
	RunRunning         RunStatus = "running"
	RunCompleted       RunStatus = "completed"
	RunFailed          RunStatus = "failed"
	RunAborted         RunStatus = "aborted"
	RunRecoverable     RunStatus = "recoverable"
	RunWaitingApproval RunStatus = "waiting_approval"
)

// Snapshot is the durable unit exchanged between workers. Agent contains
// transcript and execution configuration; Result is set once a request has a
// terminal outcome. Secrets, providers, tools, hooks and live contexts never
// belong in this value.
type Snapshot struct {
	SchemaVersion  int           `json:"schema_version"`
	Version        uint64        `json:"version"`
	RunID          string        `json:"run_id"`
	SessionID      string        `json:"session_id,omitempty"`
	TenantID       string        `json:"tenant_id,omitempty"`
	WorkspaceID    string        `json:"workspace_id,omitempty"`
	ProjectID      string        `json:"project_id,omitempty"`
	Agent          AgentSnapshot `json:"agent"`
	Result         *RunResult    `json:"result,omitempty"`
	Status         RunStatus     `json:"status"`
	IdempotencyKey string        `json:"idempotency_key,omitempty"`
	Owner          string        `json:"owner,omitempty"`
	LeaseExpiresAt time.Time     `json:"lease_expires_at,omitempty"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// NewSnapshot creates a version-zero durable snapshot.
func NewSnapshot(runID string, state AgentSnapshot) Snapshot {
	if state.SchemaVersion == 0 {
		state.SchemaVersion = CurrentSnapshotSchemaVersion
	}
	state.RunID = runID
	tenantID := state.ContextRequest.TenantID
	if state.PolicyIdentity.TenantID != "" {
		tenantID = state.PolicyIdentity.TenantID
	}
	workspaceID := state.ContextRequest.WorkspaceID
	if state.PolicyIdentity.WorkspaceID != "" {
		workspaceID = state.PolicyIdentity.WorkspaceID
	}
	sessionID := state.SessionID
	if sessionID == "" {
		sessionID = state.ContextRequest.SessionID
	}
	if state.PolicyIdentity.SessionID != "" {
		sessionID = state.PolicyIdentity.SessionID
	}
	return Snapshot{
		SchemaVersion: CurrentSnapshotSchemaVersion,
		RunID:         runID,
		SessionID:     sessionID,
		TenantID:      tenantID,
		WorkspaceID:   workspaceID,
		ProjectID:     state.ContextRequest.ProjectID,
		Agent:         state,
		Status:        RunPending,
		UpdatedAt:     time.Now().UTC(),
	}
}

// Validate checks the version and identity invariants before a snapshot is
// accepted by a store or restored by another worker.
func (s Snapshot) Validate() error {
	migrated, err := s.Migrate()
	if err != nil {
		return err
	}
	s = migrated
	if strings.TrimSpace(s.RunID) == "" {
		return fmt.Errorf("%w: run_id is required", ErrInvalidSnapshot)
	}
	if s.Agent.RunID != "" && s.Agent.RunID != s.RunID {
		return fmt.Errorf("%w: agent run_id mismatch", ErrInvalidSnapshot)
	}
	if s.Status != "" && !validRunStatus(s.Status) {
		return fmt.Errorf("%w: status is required", ErrInvalidSnapshot)
	}
	if err := s.Agent.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSnapshot, err)
	}
	if s.TenantID != "" && s.Agent.ContextRequest.TenantID != "" && s.TenantID != s.Agent.ContextRequest.TenantID {
		return fmt.Errorf("%w: tenant scope mismatch", ErrInvalidSnapshot)
	}
	if s.TenantID != "" && s.Agent.PolicyIdentity.TenantID != "" && s.TenantID != s.Agent.PolicyIdentity.TenantID {
		return fmt.Errorf("%w: tenant identity mismatch", ErrInvalidSnapshot)
	}
	if s.WorkspaceID != "" && s.Agent.ContextRequest.WorkspaceID != "" && s.WorkspaceID != s.Agent.ContextRequest.WorkspaceID {
		return fmt.Errorf("%w: workspace scope mismatch", ErrInvalidSnapshot)
	}
	if s.WorkspaceID != "" && s.Agent.PolicyIdentity.WorkspaceID != "" && s.WorkspaceID != s.Agent.PolicyIdentity.WorkspaceID {
		return fmt.Errorf("%w: workspace identity mismatch", ErrInvalidSnapshot)
	}
	if s.SessionID != "" && s.Agent.ContextRequest.SessionID != "" && s.SessionID != s.Agent.ContextRequest.SessionID {
		return fmt.Errorf("%w: session scope mismatch", ErrInvalidSnapshot)
	}
	if s.SessionID != "" && s.Agent.PolicyIdentity.SessionID != "" && s.SessionID != s.Agent.PolicyIdentity.SessionID {
		return fmt.Errorf("%w: session identity mismatch", ErrInvalidSnapshot)
	}
	return nil
}

// Migrate upgrades a legacy snapshot and all persisted transcript messages.
func (s Snapshot) Migrate() (Snapshot, error) {
	if s.SchemaVersion < 0 || s.SchemaVersion > CurrentSnapshotSchemaVersion {
		return Snapshot{}, fmt.Errorf("%w: unsupported schema version %d", ErrInvalidSnapshot, s.SchemaVersion)
	}
	if s.SchemaVersion == 0 {
		s.SchemaVersion = CurrentSnapshotSchemaVersion
	}
	agentSnapshot, err := s.Agent.Migrate()
	if err != nil {
		return Snapshot{}, err
	}
	s.Agent = agentSnapshot
	if s.Result != nil {
		for i := range s.Result.Messages {
			message, err := ai.MigrateMessage(s.Result.Messages[i])
			if err != nil {
				return Snapshot{}, err
			}
			s.Result.Messages[i] = message
		}
	}
	return s, nil
}

func validRunStatus(status RunStatus) bool {
	switch status {
	case RunPending, RunRunning, RunCompleted, RunFailed, RunAborted, RunRecoverable, RunWaitingApproval:
		return true
	default:
		return false
	}
}

func normalizeSnapshot(state Snapshot, runID string) Snapshot {
	if state.SchemaVersion == 0 {
		state.SchemaVersion = CurrentSnapshotSchemaVersion
	}
	if state.RunID == "" {
		state.RunID = runID
	}
	if state.Status == "" {
		state.Status = RunPending
	}
	if state.Agent.SchemaVersion == 0 {
		state.Agent.SchemaVersion = CurrentSnapshotSchemaVersion
	}
	if state.Agent.RunID == "" {
		state.Agent.RunID = state.RunID
	}
	if state.SessionID == "" {
		state.SessionID = state.Agent.SessionID
		if state.SessionID == "" {
			state.SessionID = state.Agent.PolicyIdentity.SessionID
		}
		if state.SessionID == "" {
			state.SessionID = state.Agent.ContextRequest.SessionID
		}
	}
	if state.TenantID == "" {
		state.TenantID = state.Agent.PolicyIdentity.TenantID
		if state.TenantID == "" {
			state.TenantID = state.Agent.ContextRequest.TenantID
		}
	}
	if state.WorkspaceID == "" {
		state.WorkspaceID = state.Agent.PolicyIdentity.WorkspaceID
		if state.WorkspaceID == "" {
			state.WorkspaceID = state.Agent.ContextRequest.WorkspaceID
		}
	}
	if state.ProjectID == "" {
		state.ProjectID = state.Agent.ContextRequest.ProjectID
	}
	return state
}

func validateSnapshotScope(state Snapshot, request RunRequest) error {
	checks := []struct {
		name     string
		stored   string
		provided string
	}{
		{name: "tenant_id", stored: state.TenantID, provided: request.TenantID},
		{name: "workspace_id", stored: state.WorkspaceID, provided: request.WorkspaceID},
		{name: "project_id", stored: state.ProjectID, provided: request.ProjectID},
	}
	for _, check := range checks {
		if check.stored == "" {
			continue
		}
		if check.provided == "" || check.stored != check.provided {
			return fmt.Errorf("%w: %s does not match stored run scope", ErrInvalidSnapshot, check.name)
		}
	}
	return nil
}

func hasDurableAgentState(state AgentSnapshot) bool {
	return state.Version > 0 || len(state.Transcript) > 0 || state.Model.ID != "" ||
		state.State != "" ||
		len(state.FollowUpQueue) > 0 || len(state.PendingSteering) > 0 ||
		state.RecoverableErrMsg != "" || state.PendingApproval != nil
}

// IsTerminal reports whether a run has reached a durable outcome.
func (s Snapshot) IsTerminal() bool {
	return s.Status == RunCompleted || s.Status == RunFailed || s.Status == RunAborted
}

// IsRecoverable reports whether another worker may safely attempt recovery.
func (s Snapshot) IsRecoverable() bool {
	return s.Status == RunRecoverable
}

// StateStore provides optimistic, versioned state persistence.
type StateStore interface {
	Load(context.Context, string) (Snapshot, error)
	Save(context.Context, string, uint64, Snapshot) error
}

// EventError is a transport-safe error representation.
type EventError struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
	Cause     string `json:"cause,omitempty"`
}

// EventEnvelope is the stable wire/storage representation of an agent event.
// Payload is deliberately raw JSON so consumers do not need to import the
// implementation of a worker that produced the event.
type EventEnvelope struct {
	SchemaVersion  int             `json:"schema_version"`
	EventID        string          `json:"event_id"`
	Type           EventKind       `json:"type"`
	RunID          string          `json:"run_id"`
	SessionID      string          `json:"session_id,omitempty"`
	TurnID         string          `json:"turn_id,omitempty"`
	ToolCallID     string          `json:"tool_call_id,omitempty"`
	Sequence       uint64          `json:"sequence"`
	Timestamp      time.Time       `json:"timestamp"`
	CorrelationID  string          `json:"correlation_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	Error          *EventError     `json:"error,omitempty"`
}

// NewEventEnvelope creates an envelope with a generated event ID. A sequence
// of zero means that an EventStore may assign the next sequence atomically.
func NewEventEnvelope(eventType EventKind, runID string, sequence uint64, payload any) (EventEnvelope, error) {
	if eventType == "" || strings.TrimSpace(runID) == "" {
		return EventEnvelope{}, fmt.Errorf("%w: type and run_id are required", ErrInvalidEvent)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return EventEnvelope{}, fmt.Errorf("%w: marshal payload: %w", ErrInvalidEvent, err)
	}
	return EventEnvelope{
		SchemaVersion: CurrentEventSchemaVersion,
		EventID:       newID(),
		Type:          eventType,
		RunID:         runID,
		Sequence:      sequence,
		Timestamp:     time.Now().UTC(),
		Payload:       raw,
	}, nil
}

// Validate checks the envelope independently from any concrete event type.
func (e EventEnvelope) Validate() error {
	migrated, err := e.Migrate()
	if err != nil {
		return err
	}
	e = migrated
	if e.EventID == "" || e.Type == "" || e.RunID == "" {
		return fmt.Errorf("%w: event_id, type and run_id are required", ErrInvalidEvent)
	}
	if e.Timestamp.IsZero() {
		return fmt.Errorf("%w: timestamp is required", ErrInvalidEvent)
	}
	return nil
}

// Migrate upgrades a legacy event envelope to the current wire schema.
func (e EventEnvelope) Migrate() (EventEnvelope, error) {
	if e.SchemaVersion < 0 || e.SchemaVersion > CurrentEventSchemaVersion {
		return EventEnvelope{}, fmt.Errorf("%w: unsupported schema version %d", ErrInvalidEvent, e.SchemaVersion)
	}
	if e.SchemaVersion == 0 {
		e.SchemaVersion = CurrentEventSchemaVersion
	}
	return e, nil
}

// EventStore appends and reads ordered event envelopes. Append is atomic for
// one call and implementations must deduplicate EventID or IdempotencyKey.
type EventStore interface {
	Append(context.Context, string, []EventEnvelope) error
	Read(context.Context, string, uint64) ([]EventEnvelope, error)
}

// TranscriptStore separates the potentially large conversation from the
// execution checkpoint. It can use different retention or compression from
// StateStore while preserving the same run identity.
type TranscriptStore interface {
	LoadTranscript(context.Context, string) ([]ai.Message, error)
	SaveTranscript(context.Context, string, uint64, []ai.Message) error
}

// OperationState is the durable lifecycle of a side-effecting tool call.
// Pending means a worker claimed the operation but its outcome has not been
// reconciled. A new worker must not execute a pending operation again: it must
// reconcile it with the external side effect or explicitly record its result.
type OperationState string

const (
	OperationPending   OperationState = "pending"
	OperationCompleted OperationState = "completed"
	OperationFailed    OperationState = "failed"
)

// OperationRecord is the durable idempotency boundary for a tool call. The
// response and error are transport-safe copies; process-local handlers and
// contexts are deliberately absent.
type OperationRecord struct {
	SchemaVersion  int                 `json:"schema_version"`
	IdempotencyKey string              `json:"idempotency_key"`
	RunID          string              `json:"run_id,omitempty"`
	TurnID         string              `json:"turn_id,omitempty"`
	ToolCallID     string              `json:"tool_call_id,omitempty"`
	ToolName       string              `json:"tool_name"`
	ArgumentsHash  string              `json:"arguments_hash"`
	State          OperationState      `json:"state"`
	Response       *tools.ToolResponse `json:"response,omitempty"`
	Error          *EventError         `json:"error,omitempty"`
	StartedAt      time.Time           `json:"started_at"`
	CompletedAt    time.Time           `json:"completed_at,omitempty"`
}

// OperationStore atomically claims and reconciles side-effecting operations.
// BeginOperation returns acquired=false when the key already exists. A
// completed/failed record can be replayed; a pending record is in doubt and
// must never be executed a second time automatically.
type OperationStore interface {
	BeginOperation(context.Context, OperationRecord) (record OperationRecord, acquired bool, err error)
	CompleteOperation(context.Context, string, tools.ToolResponse, error) error
}

func (o OperationRecord) Migrate() (OperationRecord, error) {
	if o.SchemaVersion < 0 || o.SchemaVersion > CurrentOperationSchemaVersion {
		return OperationRecord{}, fmt.Errorf("%w: unsupported operation schema version %d", ErrInvalidEvent, o.SchemaVersion)
	}
	if o.SchemaVersion == 0 {
		o.SchemaVersion = CurrentOperationSchemaVersion
	}
	return o, nil
}

type operationReleaser interface {
	ReleaseOperation(context.Context, string) error
}

// OperationReconciler lets an adapter query an idempotency-aware external
// side-effect system after a worker crashes between execution and completion
// persistence. found=false leaves the operation explicitly in doubt.
type OperationReconciler interface {
	ReconcileOperation(context.Context, OperationRecord) (response tools.ToolResponse, err error, found bool)
}

// ControlStatus describes the lifecycle of a remote abort request.
type ControlStatus string

const (
	ControlNone      ControlStatus = "none"
	ControlRequested ControlStatus = "requested"
	ControlObserved  ControlStatus = "observed"
	ControlCompleted ControlStatus = "completed"
)

// ControlState is safe to expose to a remote caller and contains no process
// handles or cancellation functions.
type ControlState struct {
	SchemaVersion  int           `json:"schema_version"`
	RunID          string        `json:"run_id"`
	Version        uint64        `json:"version"`
	Status         ControlStatus `json:"status"`
	Reason         string        `json:"reason,omitempty"`
	RequestedAt    time.Time     `json:"requested_at,omitempty"`
	ObservedAt     time.Time     `json:"observed_at,omitempty"`
	CompletedAt    time.Time     `json:"completed_at,omitempty"`
	LeaseOwner     string        `json:"lease_owner,omitempty"`
	LeaseExpiresAt time.Time     `json:"lease_expires_at,omitempty"`
}

// RunControl is the minimal remote control contract. Optional lifecycle
// methods are exposed by InMemoryRunControl and can be implemented by richer
// adapters without forcing transports to know about them.
type RunControl interface {
	RequestAbort(context.Context, string) error
	GetControl(context.Context, string) (ControlState, error)
}

type controlLifecycle interface {
	ObserveAbort(context.Context, string) error
	CompleteAbort(context.Context, string, string) error
}

// InMemoryStateStore is a concurrency-safe optimistic state store suitable
// for tests, single-process workers and examples.
