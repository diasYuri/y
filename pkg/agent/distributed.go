package agent

// This file contains the transport- and backend-neutral contracts used by a
// distributed runner. The concrete Agent remains useful for embedded callers;
// AgentRunner creates one only for the duration of a request and checkpoints
// its serialisable state at safe turn boundaries.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/policy"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/providers/auth"
	"github.com/yuri/y/pkg/telemetry"
	"github.com/yuri/y/pkg/tools"
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
type InMemoryStateStore struct {
	mu     sync.Mutex
	states map[string]Snapshot
	leases map[string]Lease
}

func NewInMemoryStateStore() *InMemoryStateStore {
	return &InMemoryStateStore{states: make(map[string]Snapshot), leases: make(map[string]Lease)}
}

func (s *InMemoryStateStore) Load(ctx context.Context, runID string) (Snapshot, error) {
	if err := contextErr(ctx); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[runID]
	if !ok {
		return Snapshot{}, ErrStateNotFound
	}
	return cloneSnapshot(state)
}

func (s *InMemoryStateStore) Save(ctx context.Context, runID string, expectedVersion uint64, state Snapshot) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	state = normalizeSnapshot(state, runID)
	var migrateErr error
	state, migrateErr = state.Migrate()
	if migrateErr != nil {
		return migrateErr
	}
	if state.RunID != runID {
		return fmt.Errorf("%w: run_id mismatch", ErrInvalidSnapshot)
	}
	if err := state.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = make(map[string]Snapshot)
	}
	if s.leases == nil {
		s.leases = make(map[string]Lease)
	}
	current, exists := s.states[runID]
	if exists && current.Version != expectedVersion {
		return fmt.Errorf("%w: expected %d, got %d", ErrVersionConflict, expectedVersion, current.Version)
	}
	if !exists && expectedVersion != 0 {
		return fmt.Errorf("%w: expected %d for new state", ErrVersionConflict, expectedVersion)
	}
	state.Version = expectedVersion + 1
	state.UpdatedAt = time.Now().UTC()
	s.states[runID], _ = cloneSnapshot(state)
	return nil
}

func (s *InMemoryStateStore) AcquireLease(ctx context.Context, runID, owner string, ttl time.Duration) (Lease, error) {
	if err := contextErr(ctx); err != nil {
		return Lease{}, err
	}
	if owner == "" || ttl <= 0 {
		return Lease{}, errors.New("agent: lease owner and positive TTL are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leases == nil {
		s.leases = make(map[string]Lease)
	}
	now := time.Now().UTC()
	if current, ok := s.leases[runID]; ok && current.ExpiresAt.After(now) && current.Owner != owner {
		return Lease{}, ErrLeaseHeld
	}
	lease := Lease{RunID: runID, Owner: owner, ExpiresAt: now.Add(ttl)}
	s.leases[runID] = lease
	return lease, nil
}

func (s *InMemoryStateStore) RenewLease(ctx context.Context, lease Lease, ttl time.Duration) (Lease, error) {
	if err := contextErr(ctx); err != nil {
		return Lease{}, err
	}
	if ttl <= 0 {
		return Lease{}, errors.New("agent: lease TTL must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.leases[lease.RunID]
	if !ok || current.Owner != lease.Owner || !current.ExpiresAt.After(time.Now().UTC()) {
		return Lease{}, ErrLeaseNotFound
	}
	current.ExpiresAt = time.Now().UTC().Add(ttl)
	s.leases[lease.RunID] = current
	return current, nil
}

func (s *InMemoryStateStore) ReleaseLease(ctx context.Context, lease Lease) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.leases[lease.RunID]
	if !ok || current.Owner != lease.Owner {
		return ErrLeaseNotFound
	}
	delete(s.leases, lease.RunID)
	return nil
}

// NoopStore makes the persistence boundary explicit for fully ephemeral runs.
// Load reports absence; Save and event operations succeed without retaining data.
type NoopStore struct{}

func (NoopStore) Load(ctx context.Context, _ string) (Snapshot, error) {
	if err := contextErr(ctx); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{}, ErrStateNotFound
}
func (NoopStore) Save(ctx context.Context, _ string, _ uint64, _ Snapshot) error {
	return contextErr(ctx)
}
func (NoopStore) Append(ctx context.Context, _ string, _ []EventEnvelope) error {
	return contextErr(ctx)
}
func (NoopStore) Read(ctx context.Context, _ string, _ uint64) ([]EventEnvelope, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	return nil, nil
}
func (NoopStore) BeginOperation(ctx context.Context, operation OperationRecord) (OperationRecord, bool, error) {
	if err := contextErr(ctx); err != nil {
		return OperationRecord{}, false, err
	}
	if err := validateOperation(operation); err != nil {
		return OperationRecord{}, false, err
	}
	operation.SchemaVersion = CurrentOperationSchemaVersion
	operation.State = OperationPending
	operation.StartedAt = time.Now().UTC()
	return operation, true, nil
}
func (NoopStore) CompleteOperation(ctx context.Context, _ string, _ tools.ToolResponse, _ error) error {
	return contextErr(ctx)
}
func (NoopStore) ReleaseOperation(ctx context.Context, _ string) error { return contextErr(ctx) }
func (NoopStore) RequestAbort(ctx context.Context, _ string) error     { return contextErr(ctx) }
func (NoopStore) GetControl(ctx context.Context, runID string) (ControlState, error) {
	if err := contextErr(ctx); err != nil {
		return ControlState{}, err
	}
	return ControlState{SchemaVersion: 1, RunID: runID, Status: ControlNone}, nil
}

// InMemoryEventStore implements ordered, idempotent append and replay.
type InMemoryEventStore struct {
	mu         sync.Mutex
	events     map[string][]EventEnvelope
	operations map[string]OperationRecord
}

func NewInMemoryEventStore() *InMemoryEventStore {
	return &InMemoryEventStore{
		events:     make(map[string][]EventEnvelope),
		operations: make(map[string]OperationRecord),
	}
}

func (s *InMemoryEventStore) Append(ctx context.Context, runID string, events []EventEnvelope) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.events == nil {
		s.events = make(map[string][]EventEnvelope)
	}
	current := s.events[runID]
	seenID := make(map[string]struct{}, len(current))
	seenKey := make(map[string]struct{}, len(current))
	for _, event := range current {
		seenID[event.EventID] = struct{}{}
		if event.IdempotencyKey != "" {
			seenKey[event.IdempotencyKey] = struct{}{}
		}
	}
	for i := range events {
		event := events[i]
		if event.RunID == "" {
			event.RunID = runID
		}
		if event.RunID != runID {
			return fmt.Errorf("%w: run_id mismatch", ErrInvalidEvent)
		}
		if event.SchemaVersion == 0 {
			event.SchemaVersion = CurrentEventSchemaVersion
		}
		if _, ok := seenID[event.EventID]; ok {
			continue
		}
		if event.IdempotencyKey != "" {
			if _, ok := seenKey[event.IdempotencyKey]; ok {
				continue
			}
		}
		if event.Sequence == 0 {
			event.Sequence = uint64(len(current) + 1)
		}
		if len(current) > 0 && event.Sequence != current[len(current)-1].Sequence+1 {
			if _, ok := seenID[event.EventID]; ok || event.IdempotencyKey != "" && hasKey(current, event.IdempotencyKey) {
				continue
			}
			return fmt.Errorf("%w: expected %d, got %d", ErrEventOutOfOrder, current[len(current)-1].Sequence+1, event.Sequence)
		}
		if err := event.Validate(); err != nil {
			return err
		}
		current = append(current, event)
		seenID[event.EventID] = struct{}{}
		if event.IdempotencyKey != "" {
			seenKey[event.IdempotencyKey] = struct{}{}
		}
	}
	s.events[runID] = current
	return nil
}

func (s *InMemoryEventStore) Read(ctx context.Context, runID string, after uint64) ([]EventEnvelope, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []EventEnvelope
	for _, event := range s.events[runID] {
		if event.Sequence > after {
			out = append(out, cloneEvent(event))
		}
	}
	return out, nil
}

// BeginOperation implements OperationStore with the same mutex as the event
// log, so two workers sharing this store cannot both claim one key.
func (s *InMemoryEventStore) BeginOperation(ctx context.Context, requested OperationRecord) (OperationRecord, bool, error) {
	if err := contextErr(ctx); err != nil {
		return OperationRecord{}, false, err
	}
	if err := validateOperation(requested); err != nil {
		return OperationRecord{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.operations == nil {
		s.operations = make(map[string]OperationRecord)
	}
	if existing, ok := s.operations[requested.IdempotencyKey]; ok {
		if err := compareOperationIdentity(existing, requested); err != nil {
			return OperationRecord{}, false, err
		}
		return cloneOperation(existing), false, nil
	}
	requested.SchemaVersion = CurrentOperationSchemaVersion
	requested.State = OperationPending
	requested.StartedAt = time.Now().UTC()
	s.operations[requested.IdempotencyKey] = cloneOperation(requested)
	return cloneOperation(requested), true, nil
}

func (s *InMemoryEventStore) CompleteOperation(ctx context.Context, key string, response tools.ToolResponse, operationErr error) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	operation, ok := s.operations[key]
	if !ok {
		return ErrOperationNotFound
	}
	if operation.State != OperationPending {
		return nil
	}
	operation.Response = cloneToolResponsePtr(response)
	if operationErr != nil {
		operation.State = OperationFailed
		operation.Error = structuredOperationError(operationErr)
	} else {
		operation.State = OperationCompleted
	}
	operation.CompletedAt = time.Now().UTC()
	s.operations[key] = cloneOperation(operation)
	return nil
}

func (s *InMemoryEventStore) ReleaseOperation(ctx context.Context, key string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	operation, ok := s.operations[key]
	if !ok || operation.State != OperationPending {
		return ErrOperationNotFound
	}
	delete(s.operations, key)
	return nil
}

// InMemoryRunControl is a remote-abort adapter with idempotent requests.
type InMemoryRunControl struct {
	mu     sync.Mutex
	states map[string]ControlState
	now    func() time.Time
}

func NewInMemoryRunControl() *InMemoryRunControl {
	return &InMemoryRunControl{states: make(map[string]ControlState), now: time.Now}
}

func (c *InMemoryRunControl) RequestAbort(ctx context.Context, runID string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.states == nil {
		c.states = make(map[string]ControlState)
	}
	state := c.states[runID]
	if state.RunID == "" {
		state = ControlState{SchemaVersion: 1, RunID: runID, Status: ControlNone}
	}
	if state.Status == ControlCompleted {
		return nil
	}
	now := c.clock()
	if state.Status != ControlRequested && state.Status != ControlObserved {
		state.Version++
		state.Status = ControlRequested
		state.RequestedAt = now
	}
	c.states[runID] = state
	return nil
}

func (c *InMemoryRunControl) GetControl(ctx context.Context, runID string) (ControlState, error) {
	if err := contextErr(ctx); err != nil {
		return ControlState{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.states == nil {
		c.states = make(map[string]ControlState)
	}
	state := c.states[runID]
	if state.RunID == "" {
		state = ControlState{SchemaVersion: 1, RunID: runID, Status: ControlNone}
	}
	return state, nil
}

func (c *InMemoryRunControl) ObserveAbort(ctx context.Context, runID string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.states == nil {
		c.states = make(map[string]ControlState)
	}
	state := c.states[runID]
	if state.Status == ControlRequested {
		state.Version++
		state.Status = ControlObserved
		state.ObservedAt = c.clock()
		c.states[runID] = state
	}
	return nil
}

func (c *InMemoryRunControl) CompleteAbort(ctx context.Context, runID, reason string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[runID]
	if state.Status == ControlCompleted {
		return nil
	}
	state.RunID = runID
	state.SchemaVersion = 1
	state.Version++
	state.Status = ControlCompleted
	state.Reason = reason
	state.CompletedAt = c.clock()
	c.states[runID] = state
	return nil
}

func (c *InMemoryRunControl) clock() time.Time {
	if c.now == nil {
		return time.Now().UTC()
	}
	return c.now().UTC()
}

// Lease is an ownership assertion used to prevent two workers from executing
// the same run concurrently. Stores may implement LeaseStore to make this
// atomic across processes.
type Lease struct {
	RunID     string    `json:"run_id"`
	Owner     string    `json:"owner"`
	ExpiresAt time.Time `json:"expires_at"`
}

type LeaseStore interface {
	AcquireLease(context.Context, string, string, time.Duration) (Lease, error)
	RenewLease(context.Context, Lease, time.Duration) (Lease, error)
	ReleaseLease(context.Context, Lease) error
}

// JSONLStore is a local development adapter. State is atomically replaced as
// JSON and events are appended as one JSON object per line. It intentionally
// exposes the same contracts that a SQL/object-store adapter would implement.
type JSONLStore struct {
	root string
	mu   sync.Mutex
}

func NewJSONLStore(root string) *JSONLStore { return &JSONLStore{root: root} }

const jsonlLockStaleAfter = 5 * time.Minute

func (s *JSONLStore) runDir(runID string) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(runID))
	return filepath.Join(s.root, "runs", "r-"+encoded)
}

func (s *JSONLStore) legacyRunDir(runID string) (string, bool) {
	if runID == "" || runID == "." || runID == ".." {
		return "", false
	}
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, runID)
	if clean != runID {
		return "", false
	}
	return filepath.Join(s.root, clean), true
}

func (s *JSONLStore) readFile(runID, name string) ([]byte, error) {
	path := filepath.Join(s.runDir(runID), name)
	raw, err := os.ReadFile(path)
	if !errors.Is(err, os.ErrNotExist) {
		return raw, err
	}
	legacyDir, ok := s.legacyRunDir(runID)
	if !ok {
		return nil, err
	}
	return os.ReadFile(filepath.Join(legacyDir, name))
}

func (s *JSONLStore) Load(ctx context.Context, runID string) (Snapshot, error) {
	if err := contextErr(ctx); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return Snapshot{}, err
	}
	defer release()
	raw, err := s.readFile(runID, "state.json")
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, ErrStateNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	var state Snapshot
	if err := json.Unmarshal(raw, &state); err != nil {
		return Snapshot{}, fmt.Errorf("decode state: %w", err)
	}
	if state.RunID != runID {
		return Snapshot{}, fmt.Errorf("%w: stored run_id %q does not match %q", ErrInvalidSnapshot, state.RunID, runID)
	}
	state = normalizeSnapshot(state, runID)
	var migrateErr error
	state, migrateErr = state.Migrate()
	if migrateErr != nil {
		return Snapshot{}, migrateErr
	}
	return state, state.Validate()
}

func (s *JSONLStore) Save(ctx context.Context, runID string, expectedVersion uint64, state Snapshot) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	current, err := s.loadUnlocked(runID)
	switch {
	case errors.Is(err, ErrStateNotFound):
		if expectedVersion != 0 {
			return ErrVersionConflict
		}
	case err != nil:
		return err
	case current.Version != expectedVersion:
		return fmt.Errorf("%w: expected %d, got %d", ErrVersionConflict, expectedVersion, current.Version)
	}
	state = normalizeSnapshot(state, runID)
	var migrateErr error
	state, migrateErr = state.Migrate()
	if migrateErr != nil {
		return migrateErr
	}
	if err := state.Validate(); err != nil {
		return err
	}
	state.Version = expectedVersion + 1
	state.UpdatedAt = time.Now().UTC()
	return atomicJSONWrite(filepath.Join(s.runDir(runID), "state.json"), state)
}

func (s *JSONLStore) loadUnlocked(runID string) (Snapshot, error) {
	raw, err := s.readFile(runID, "state.json")
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, ErrStateNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	var state Snapshot
	if err := json.Unmarshal(raw, &state); err != nil {
		return Snapshot{}, err
	}
	if state.RunID != runID {
		return Snapshot{}, fmt.Errorf("%w: stored run_id %q does not match %q", ErrInvalidSnapshot, state.RunID, runID)
	}
	state = normalizeSnapshot(state, runID)
	var migrateErr error
	state, migrateErr = state.Migrate()
	if migrateErr != nil {
		return Snapshot{}, migrateErr
	}
	return state, state.Validate()
}

func (s *JSONLStore) Append(ctx context.Context, runID string, events []EventEnvelope) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	path := filepath.Join(s.runDir(runID), "events.jsonl")
	contents, err := s.readFile(runID, "events.jsonl")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var existing []EventEnvelope
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event EventEnvelope
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return fmt.Errorf("decode event: %w", err)
		}
		migrated, err := event.Migrate()
		if err != nil {
			return err
		}
		event = migrated
		if event.RunID != runID {
			return fmt.Errorf("%w: stored run_id %q does not match %q", ErrInvalidEvent, event.RunID, runID)
		}
		if err := event.Validate(); err != nil {
			return err
		}
		existing = append(existing, event)
	}
	store := NewInMemoryEventStore()
	store.events[runID] = existing
	if err := store.Append(ctx, runID, events); err != nil {
		return err
	}
	if len(store.events[runID]) == len(existing) {
		return nil
	}
	if err := os.MkdirAll(s.runDir(runID), 0o700); err != nil {
		return err
	}
	var canonical strings.Builder
	for _, event := range store.events[runID] {
		line, err := json.Marshal(event)
		if err != nil {
			return err
		}
		canonical.Write(line)
		canonical.WriteByte('\n')
	}
	// Rewriting the small local log through temp-file/rename makes one Append
	// call atomic even when it contains a batch.
	return atomicWriteBytes(path, []byte(canonical.String()), 0o600)
}

func (s *JSONLStore) Read(ctx context.Context, runID string, after uint64) ([]EventEnvelope, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return nil, err
	}
	defer release()
	raw, err := s.readFile(runID, "events.jsonl")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []EventEnvelope
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event EventEnvelope
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("decode event: %w", err)
		}
		migrated, err := event.Migrate()
		if err != nil {
			return nil, err
		}
		event = migrated
		if event.RunID != runID {
			return nil, fmt.Errorf("%w: stored run_id %q does not match %q", ErrInvalidEvent, event.RunID, runID)
		}
		if err := event.Validate(); err != nil {
			return nil, err
		}
		if event.Sequence > after {
			out = append(out, event)
		}
	}
	return out, nil
}

// BeginOperation implements OperationStore using a compare-and-create update
// under the same cross-process run lock as state and events. The operation log
// is a compact JSON map because it is a local development adapter, while the
// contract remains suitable for a transactional external implementation.
func (s *JSONLStore) BeginOperation(ctx context.Context, requested OperationRecord) (OperationRecord, bool, error) {
	if err := contextErr(ctx); err != nil {
		return OperationRecord{}, false, err
	}
	if err := validateOperation(requested); err != nil {
		return OperationRecord{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, requested.RunID)
	if err != nil {
		return OperationRecord{}, false, err
	}
	defer release()

	operations, err := s.loadOperationsUnlocked(requested.RunID)
	if err != nil {
		return OperationRecord{}, false, err
	}
	if existing, ok := operations[requested.IdempotencyKey]; ok {
		if err := compareOperationIdentity(existing, requested); err != nil {
			return OperationRecord{}, false, err
		}
		return cloneOperation(existing), false, nil
	}
	requested.SchemaVersion = CurrentOperationSchemaVersion
	requested.State = OperationPending
	requested.StartedAt = time.Now().UTC()
	operations[requested.IdempotencyKey] = cloneOperation(requested)
	if err := s.saveOperationsUnlocked(requested.RunID, operations); err != nil {
		return OperationRecord{}, false, err
	}
	return cloneOperation(requested), true, nil
}

func (s *JSONLStore) CompleteOperation(ctx context.Context, key string, response tools.ToolResponse, operationErr error) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if key == "" {
		return ErrOperationNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The operation key contains the run ID in Agent's default construction,
	// but custom callers may not. Search the small local run tree in that case.
	runID, operation, err := s.findOperationUnlocked(key)
	if err != nil {
		return err
	}
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	operations, err := s.loadOperationsUnlocked(runID)
	if err != nil {
		return err
	}
	operation = operations[key]
	if operation.State != OperationPending {
		return nil
	}
	operation.Response = cloneToolResponsePtr(response)
	if operationErr != nil {
		operation.State = OperationFailed
		operation.Error = structuredOperationError(operationErr)
	} else {
		operation.State = OperationCompleted
	}
	operation.CompletedAt = time.Now().UTC()
	operations[key] = cloneOperation(operation)
	return s.saveOperationsUnlocked(runID, operations)
}

func (s *JSONLStore) ReleaseOperation(ctx context.Context, key string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if key == "" {
		return ErrOperationNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	runID, operation, err := s.findOperationUnlocked(key)
	if err != nil {
		return err
	}
	if operation.State != OperationPending {
		return ErrOperationNotFound
	}
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	operations, err := s.loadOperationsUnlocked(runID)
	if err != nil {
		return err
	}
	delete(operations, key)
	return s.saveOperationsUnlocked(runID, operations)
}

func (s *JSONLStore) loadOperationsUnlocked(runID string) (map[string]OperationRecord, error) {
	raw, err := s.readFile(runID, "operations.json")
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]OperationRecord), nil
	}
	if err != nil {
		return nil, err
	}
	var operations map[string]OperationRecord
	if err := json.Unmarshal(raw, &operations); err != nil {
		return nil, fmt.Errorf("decode operations: %w", err)
	}
	if operations == nil {
		operations = make(map[string]OperationRecord)
	}
	for key, operation := range operations {
		migrated, err := operation.Migrate()
		if err != nil {
			return nil, fmt.Errorf("operation %q: %w", key, err)
		}
		operations[key] = migrated
		if err := validateOperation(migrated); err != nil {
			return nil, fmt.Errorf("operation %q: %w", key, err)
		}
	}
	return operations, nil
}

func (s *JSONLStore) saveOperationsUnlocked(runID string, operations map[string]OperationRecord) error {
	return atomicJSONWrite(filepath.Join(s.runDir(runID), "operations.json"), operations)
}

// AcquireLease implements LeaseStore with a lease record protected by the
// same cross-process lock used for state and events.
func (s *JSONLStore) AcquireLease(ctx context.Context, runID, owner string, ttl time.Duration) (Lease, error) {
	if err := contextErr(ctx); err != nil {
		return Lease{}, err
	}
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(owner) == "" || ttl <= 0 {
		return Lease{}, errors.New("agent: run ID, lease owner and positive TTL are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return Lease{}, err
	}
	defer release()
	current, err := s.loadLeaseUnlocked(runID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Lease{}, err
	}
	now := time.Now().UTC()
	if current.Owner != "" && current.ExpiresAt.After(now) && current.Owner != owner {
		return Lease{}, ErrLeaseHeld
	}
	lease := Lease{RunID: runID, Owner: owner, ExpiresAt: now.Add(ttl)}
	if err := s.saveLeaseUnlocked(lease); err != nil {
		return Lease{}, err
	}
	return lease, nil
}

func (s *JSONLStore) RenewLease(ctx context.Context, lease Lease, ttl time.Duration) (Lease, error) {
	if err := contextErr(ctx); err != nil {
		return Lease{}, err
	}
	if lease.RunID == "" || lease.Owner == "" || ttl <= 0 {
		return Lease{}, errors.New("agent: valid lease and positive TTL are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, lease.RunID)
	if err != nil {
		return Lease{}, err
	}
	defer release()
	current, err := s.loadLeaseUnlocked(lease.RunID)
	if errors.Is(err, os.ErrNotExist) {
		return Lease{}, ErrLeaseNotFound
	}
	if err != nil {
		return Lease{}, err
	}
	if current.Owner != lease.Owner || !current.ExpiresAt.After(time.Now().UTC()) {
		return Lease{}, ErrLeaseNotFound
	}
	current.ExpiresAt = time.Now().UTC().Add(ttl)
	if err := s.saveLeaseUnlocked(current); err != nil {
		return Lease{}, err
	}
	return current, nil
}

func (s *JSONLStore) ReleaseLease(ctx context.Context, lease Lease) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if lease.RunID == "" || lease.Owner == "" {
		return ErrLeaseNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, lease.RunID)
	if err != nil {
		return err
	}
	defer release()
	current, err := s.loadLeaseUnlocked(lease.RunID)
	if errors.Is(err, os.ErrNotExist) {
		return ErrLeaseNotFound
	}
	if err != nil {
		return err
	}
	if current.Owner != lease.Owner {
		return ErrLeaseNotFound
	}
	return os.Remove(filepath.Join(s.runDir(lease.RunID), "lease.json"))
}

func (s *JSONLStore) loadLeaseUnlocked(runID string) (Lease, error) {
	raw, err := s.readFile(runID, "lease.json")
	if err != nil {
		return Lease{}, err
	}
	var lease Lease
	if err := json.Unmarshal(raw, &lease); err != nil {
		return Lease{}, fmt.Errorf("decode lease: %w", err)
	}
	return lease, nil
}

func (s *JSONLStore) saveLeaseUnlocked(lease Lease) error {
	return atomicJSONWrite(filepath.Join(s.runDir(lease.RunID), "lease.json"), lease)
}

type transcriptRecord struct {
	SchemaVersion int          `json:"schema_version"`
	Version       uint64       `json:"version"`
	Messages      []ai.Message `json:"messages,omitempty"`
}

func (s *JSONLStore) LoadTranscript(ctx context.Context, runID string) ([]ai.Message, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return nil, err
	}
	defer release()
	raw, err := s.readFile(runID, "transcript.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrTranscriptNotFound
	}
	if err != nil {
		return nil, err
	}
	var record transcriptRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("decode transcript: %w", err)
	}
	for i := range record.Messages {
		message, err := ai.MigrateMessage(record.Messages[i])
		if err != nil {
			return nil, err
		}
		record.Messages[i] = message
	}
	return cloneMessages(record.Messages), nil
}

func (s *JSONLStore) SaveTranscript(ctx context.Context, runID string, expectedVersion uint64, messages []ai.Message) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	currentVersion := uint64(0)
	raw, readErr := s.readFile(runID, "transcript.json")
	if readErr == nil {
		var current transcriptRecord
		if err := json.Unmarshal(raw, &current); err != nil {
			return fmt.Errorf("decode transcript: %w", err)
		}
		currentVersion = current.Version
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if readErr == nil && currentVersion != expectedVersion {
		return ErrVersionConflict
	}
	return atomicJSONWrite(filepath.Join(s.runDir(runID), "transcript.json"), transcriptRecord{
		SchemaVersion: ai.CurrentSchemaVersion,
		Version:       expectedVersion,
		Messages:      cloneMessages(messages),
	})
}

func (s *JSONLStore) findOperationUnlocked(key string) (string, OperationRecord, error) {
	runsRoot := filepath.Join(s.root, "runs")
	entries, err := os.ReadDir(runsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return "", OperationRecord{}, ErrOperationNotFound
	}
	if err != nil {
		return "", OperationRecord{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "r-") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(runsRoot, entry.Name(), "operations.json"))
		if readErr != nil {
			continue
		}
		var operations map[string]OperationRecord
		if json.Unmarshal(raw, &operations) == nil {
			if operation, ok := operations[key]; ok {
				runIDBytes, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(entry.Name(), "r-"))
				if decodeErr != nil {
					return "", OperationRecord{}, decodeErr
				}
				return string(runIDBytes), operation, nil
			}
		}
	}
	return "", OperationRecord{}, ErrOperationNotFound
}

// acquireRunLock supplies the cross-process serialization missing from a
// process-local sync.Mutex. The lock file contains no sensitive state and is
// removed on normal release; an old lock is treated as abandoned.
func (s *JSONLStore) acquireRunLock(ctx context.Context, runID string) (func(), error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	lockDir := filepath.Join(s.root, "locks")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, err
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(runID))
	path := filepath.Join(lockDir, "r-"+encoded+".lock")
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, _ = file.WriteString(time.Now().UTC().Format(time.RFC3339Nano))
			_ = file.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > jsonlLockStaleAfter {
			_ = os.Remove(path)
			continue
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func atomicJSONWrite(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return atomicWriteBytes(path, raw, 0o600)
}

func atomicWriteBytes(path string, raw []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// AgentRunner is a stateless-by-default execution facade. It rehydrates a
// short-lived Agent, observes remote abort state, emits versioned events and
// saves a terminal/checkpoint snapshot. A worker may therefore disappear
// between calls without making the session depend on its goroutines.
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

func (r *AgentRunner) Run(ctx context.Context, request RunRequest) (RunResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.RunID == "" {
		request.RunID = newID()
	}
	lock := r.lockFor(request.RunID)
	lock.Lock()
	defer lock.Unlock()

	state, expectedVersion, err := r.loadInitial(ctx, request)
	if err != nil {
		return RunResponse{}, err
	}
	if request.TenantID == "" {
		request.TenantID = request.Identity.TenantID
	}
	if request.WorkspaceID == "" {
		request.WorkspaceID = request.Identity.WorkspaceID
	}
	if request.SessionID == "" {
		request.SessionID = request.Identity.SessionID
	}
	if request.Identity.TenantID != "" && request.Identity.TenantID != request.TenantID {
		return RunResponse{}, fmt.Errorf("%w: identity tenant mismatch", ErrInvalidSnapshot)
	}
	if request.Identity.WorkspaceID != "" && request.Identity.WorkspaceID != request.WorkspaceID {
		return RunResponse{}, fmt.Errorf("%w: identity workspace mismatch", ErrInvalidSnapshot)
	}
	if request.Identity.RunID != "" && request.Identity.RunID != request.RunID {
		return RunResponse{}, fmt.Errorf("%w: identity run_id mismatch", ErrInvalidSnapshot)
	}
	if err := validateSnapshotScope(state, request); err != nil {
		return RunResponse{}, err
	}
	if state.Result != nil && state.IsTerminal() {
		return RunResponse{RunID: request.RunID, Snapshot: state, Result: *state.Result}, nil
	}
	provider := r.provider
	providerID := request.ProviderID
	if request.Model.Provider != "" {
		providerID = request.Model.Provider
	}
	if providerID == "" && state.Agent.Model.Provider != "" {
		providerID = state.Agent.Model.Provider
	}
	if providerID == "" && provider != nil {
		providerID = ai.ProviderID(provider.ID())
	}
	if provider == nil || (providerID != "" && ai.ProviderID(provider.ID()) != providerID) {
		if r.providerResolver == nil {
			return RunResponse{}, fmt.Errorf("agent: provider %q is not configured", providerID)
		}
		provider, err = r.providerResolver.ResolveProvider(ctx, providerID)
		if err != nil {
			return RunResponse{}, err
		}
		if provider == nil {
			return RunResponse{}, fmt.Errorf("agent: provider %q resolver returned nil", providerID)
		}
	}
	model := request.Model
	if model.ID == "" && request.ModelID != "" {
		if r.modelCatalog == nil {
			return RunResponse{}, errors.New("agent: model catalog is not configured")
		}
		model, err = r.modelCatalog.Lookup(ctx, providerID, request.ModelID)
		if err != nil {
			return RunResponse{}, err
		}
	}
	if model.Provider != "" && model.Provider != ai.ProviderID(provider.ID()) {
		return RunResponse{}, fmt.Errorf("agent: model provider %q does not match runner provider %q", model.Provider, provider.ID())
	}
	if model.Provider == "" {
		model.Provider = ai.ProviderID(provider.ID())
	}
	apiKey := request.APIKey
	if apiKey == "" && r.credentials != nil {
		providerID := ""
		if model.Provider != "" {
			providerID = string(model.Provider)
		} else {
			providerID = provider.ID()
		}
		apiKey, err = r.credentials.ResolveAPIKey(ctx, auth.ResolveRequest{
			ProviderID: providerID,
			TenantID:   request.TenantID,
		})
		if err != nil {
			return RunResponse{}, err
		}
	}
	restoreAgent := hasDurableAgentState(state.Agent)
	recoverAgent := restoreAgent && state.Agent.State == StateFailed && state.Agent.RecoverableErrMsg != ""

	identity := clonePolicyIdentity(state.Agent.PolicyIdentity)
	if request.Identity.CallerID != "" {
		identity.CallerID = request.Identity.CallerID
	}
	if request.TenantID != "" {
		identity.TenantID = request.TenantID
	}
	if request.WorkspaceID != "" {
		identity.WorkspaceID = request.WorkspaceID
	}
	if request.SessionID != "" {
		identity.SessionID = request.SessionID
	}
	if request.Identity.RunID != "" {
		identity.RunID = request.Identity.RunID
	}
	if request.Identity.Capabilities != nil {
		identity.Capabilities = append([]string(nil), request.Identity.Capabilities...)
	}
	if identity.RunID == "" {
		identity.RunID = request.RunID
	}
	policyVersion := request.PolicyVersion
	if policyVersion == "" {
		policyVersion = state.Agent.PolicyVersion
	}
	effectiveIdempotencyKey := request.IdempotencyKey
	if effectiveIdempotencyKey == "" {
		effectiveIdempotencyKey = state.IdempotencyKey
		if effectiveIdempotencyKey == "" {
			effectiveIdempotencyKey = state.Agent.IdempotencyKey
		}
	}
	runNonce := state.Agent.RunNonce
	if runNonce == "" {
		runNonce = newID()
	}
	if state.Status == RunRunning && state.LeaseExpiresAt.After(time.Now().UTC()) && state.Owner != "" && state.Owner != r.owner {
		return RunResponse{}, ErrRunInProgress
	}
	var lease Lease
	if leaseStore, ok := r.stateStore.(LeaseStore); ok {
		lease, err = leaseStore.AcquireLease(ctx, request.RunID, r.owner, 2*time.Minute)
		if err != nil {
			return RunResponse{}, err
		}
		defer func() { _ = leaseStore.ReleaseLease(context.Background(), lease) }()
	}
	state.RunID = request.RunID
	state.SchemaVersion = CurrentSnapshotSchemaVersion
	state.Status = RunRunning
	state.Owner = r.owner
	state.LeaseExpiresAt = time.Now().UTC().Add(2 * time.Minute)
	state.IdempotencyKey = effectiveIdempotencyKey
	state.Agent.RunID = request.RunID
	state.Agent.RunNonce = runNonce
	state.Agent.PolicyIdentity = clonePolicyIdentity(identity)
	state.Agent.PolicyVersion = policyVersion
	state.Agent.IdempotencyKey = effectiveIdempotencyKey
	if !request.AuthorizationExpiresAt.IsZero() {
		state.Agent.AuthorizationExpiresAt = request.AuthorizationExpiresAt
	}
	if request.SessionID != "" {
		state.SessionID = request.SessionID
		state.Agent.SessionID = request.SessionID
		state.Agent.ContextRequest.SessionID = request.SessionID
	}
	if request.TenantID != "" {
		if state.TenantID != "" && state.TenantID != request.TenantID {
			return RunResponse{}, fmt.Errorf("%w: tenant mismatch", ErrInvalidSnapshot)
		}
		state.TenantID = request.TenantID
		state.Agent.ContextRequest.TenantID = request.TenantID
	}
	if request.WorkspaceID != "" {
		if state.WorkspaceID != "" && state.WorkspaceID != request.WorkspaceID {
			return RunResponse{}, fmt.Errorf("%w: workspace mismatch", ErrInvalidSnapshot)
		}
		state.WorkspaceID = request.WorkspaceID
		state.Agent.ContextRequest.WorkspaceID = request.WorkspaceID
	}
	if request.ProjectID != "" {
		if state.ProjectID != "" && state.ProjectID != request.ProjectID {
			return RunResponse{}, fmt.Errorf("%w: project mismatch", ErrInvalidSnapshot)
		}
		state.ProjectID = request.ProjectID
		state.Agent.ContextRequest.ProjectID = request.ProjectID
	}
	if request.RequestID != "" {
		state.Agent.ContextRequest.RequestID = request.RequestID
	}
	if model.ID != "" {
		state.Agent.Model = sanitizeModel(model)
	}
	if err := r.saveState(ctx, request.RunID, expectedVersion, state); err != nil {
		if errors.Is(err, ErrStateNotFound) {
			// NoopStore never saves state and is allowed to run without a durable checkpoint.
		} else {
			return RunResponse{}, err
		}
	}
	if state.Version == expectedVersion {
		state.Version = expectedVersion + 1
	}
	expectedVersion = state.Version

	requestOptions := []Option{WithRunID(request.RunID), WithRunNonce(runNonce)}
	if request.SessionID != "" {
		requestOptions = append(requestOptions, WithSessionID(request.SessionID))
	}
	requestOptions = append(requestOptions, WithContextIdentity(request.TenantID, request.WorkspaceID, request.ProjectID, request.SessionID, request.RequestID))
	if identity.TenantID != "" || identity.CallerID != "" || len(identity.Capabilities) > 0 {
		requestOptions = append(requestOptions, WithPolicyIdentity(identity))
	}
	if policyVersion != "" {
		requestOptions = append(requestOptions, WithPolicyVersion(policyVersion))
	}
	if !request.AuthorizationExpiresAt.IsZero() {
		requestOptions = append(requestOptions, WithAuthorizationExpiry(request.AuthorizationExpiresAt))
	}
	if effectiveIdempotencyKey != "" {
		requestOptions = append(requestOptions, WithIdempotencyKey(effectiveIdempotencyKey))
	}
	if model.ID != "" {
		requestOptions = append(requestOptions, WithModel(model))
	}
	if request.Approval != nil {
		requestOptions = append(requestOptions, WithApprovalResolution(request.Approval))
	}
	if apiKey != "" {
		requestOptions = append(requestOptions, WithStreamDefaults(providers.StreamOptions{APIKey: apiKey}))
	}
	agentOptions := append([]Option(nil), r.options...)
	agentOptions = append(agentOptions, requestOptions...)
	registry := r.registry
	if registry != nil && r.eventStore != nil {
		registry = &eventBackedToolRegistry{base: registry, events: r.eventStore, operations: r.operationStore}
	}
	ag := New(provider, registry, agentOptions...)
	if restoreAgent {
		if err := ag.Restore(state.Agent); err != nil {
			return RunResponse{}, err
		}
		// Restore is intentionally authoritative for durable settings. Request
		// identity and ephemeral credentials are the exception: they must be
		// reapplied after restore so a sparse fresh snapshot cannot erase them.
		for _, option := range requestOptions {
			if option != nil {
				option(ag)
			}
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	var monitorDone chan struct{}
	if r.control != nil {
		monitorDone = make(chan struct{})
		go r.monitorAbort(runCtx, request.RunID, cancel, monitorDone)
	}
	var leaseDone chan struct{}
	if leaseStore, ok := r.stateStore.(LeaseStore); ok && lease.RunID != "" {
		leaseDone = make(chan struct{})
		go r.monitorLease(runCtx, leaseStore, lease, cancel, leaseDone)
	}
	defer func() {
		cancel()
		if monitorDone != nil {
			<-monitorDone
		}
		if leaseDone != nil {
			<-leaseDone
		}
	}()

	var eventsMu sync.Mutex
	events := make([]EventEnvelope, 0, 16)
	sequence := uint64(0)
	var checkpointErr error
	var eventStoreErr error
	acceptEvents := true
	existing, readErr := r.eventStore.Read(ctx, request.RunID, 0)
	if readErr != nil {
		return RunResponse{}, readErr
	}
	for _, event := range existing {
		if event.Sequence > sequence {
			sequence = event.Sequence
		}
	}
	seenEventIDs := make(map[string]struct{}, len(existing))
	seenIdempotencyKeys := make(map[string]struct{}, len(existing))
	for _, event := range existing {
		if event.EventID != "" {
			seenEventIDs[event.EventID] = struct{}{}
		}
		if event.IdempotencyKey != "" {
			seenIdempotencyKeys[event.IdempotencyKey] = struct{}{}
		}
	}
	appendEnvelope := func(envelope EventEnvelope) bool {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		if envelope.EventID != "" {
			if _, exists := seenEventIDs[envelope.EventID]; exists {
				return false
			}
		}
		if envelope.IdempotencyKey != "" {
			if _, exists := seenIdempotencyKeys[envelope.IdempotencyKey]; exists {
				return false
			}
		}
		sequence++
		envelope.Sequence = sequence
		if envelope.IdempotencyKey == "" && effectiveIdempotencyKey != "" {
			envelope.IdempotencyKey = fmt.Sprintf("%s:event-%d", effectiveIdempotencyKey, sequence)
		}
		events = append(events, envelope)
		if err := r.eventStore.Append(context.Background(), request.RunID, []EventEnvelope{envelope}); err != nil {
			if eventStoreErr == nil {
				eventStoreErr = err
			}
			return true
		}
		if envelope.EventID != "" {
			seenEventIDs[envelope.EventID] = struct{}{}
		}
		if envelope.IdempotencyKey != "" {
			seenIdempotencyKeys[envelope.IdempotencyKey] = struct{}{}
		}
		return true
	}
	unsubscribe := ag.Subscribe(func(event Event) {
		switch event.Kind {
		case EventAgentStarted, EventAgentCompleted, EventAbortRequested, EventAbortCompleted,
			EventTextDelta, EventTurnEnded, EventToolEnded, EventCompleted:
			// The runner owns the canonical lifecycle envelope and normalizes
			// these legacy aliases. Direct Agent consumers still receive them.
			return
		}
		eventsMu.Lock()
		if !acceptEvents {
			eventsMu.Unlock()
			return
		}
		envelope, envelopeErr := envelopeFromEvent(event, request.RunID, sequence+1)
		eventsMu.Unlock()
		appended := false
		if envelopeErr == nil {
			appended = appendEnvelope(envelope)
		}

		if (event.Kind == EventTurnCompleted || event.Kind == EventRetryScheduled) && appended {
			checkpoint := ag.Snapshot()
			checkpoint.RunID = request.RunID
			state.Agent = checkpoint
			state.Status = RunRunning
			state.Result = nil
			state.Owner = r.owner
			state.LeaseExpiresAt = lease.ExpiresAt
			if err := r.saveState(context.Background(), request.RunID, expectedVersion, state); err != nil && !errors.Is(err, ErrStateNotFound) {
				checkpointErr = err
				return
			} else {
				state.Version = expectedVersion + 1
				expectedVersion = state.Version
				if r.transcriptStore != nil {
					if err := r.saveTranscript(context.Background(), request.RunID, state.Version, checkpoint.Transcript); err != nil && checkpointErr == nil {
						checkpointErr = err
					}
				}
				checkpointEnvelope, checkpointEnvelopeErr := NewEventEnvelope(EventStateCheckpointed, request.RunID, 0, map[string]any{
					"version": state.Version,
					"status":  RunRunning,
				})
				if checkpointEnvelopeErr == nil {
					if effectiveIdempotencyKey != "" {
						checkpointEnvelope.IdempotencyKey = fmt.Sprintf("%s:checkpoint-%d", effectiveIdempotencyKey, state.Version)
					}
					appendEnvelope(checkpointEnvelope)
				}
			}
		}
	})
	start, _ := NewEventEnvelope(EventAgentStarted, request.RunID, 0, map[string]any{"owner": r.owner})
	if effectiveIdempotencyKey != "" {
		start.IdempotencyKey = effectiveIdempotencyKey + ":agent-start"
	}
	appendEnvelope(start)

	messages := append([]ai.Message(nil), request.Messages...)
	if state.Agent.PendingApproval == nil && request.Prompt != "" {
		messages = append(messages, promptMessage(request.Prompt))
	}
	if state.Agent.PendingApproval != nil {
		messages = nil
	}
	var result RunResult
	var runErr error
	if recoverAgent {
		result, runErr = ag.Recover(runCtx)
	} else {
		result, runErr = ag.RunMessages(runCtx, messages...)
	}
	status := statusForResult(result, runErr)
	if checkpointErr != nil && runErr == nil {
		runErr = checkpointErr
		status = statusForResult(result, runErr)
	}
	state.Agent = ag.Snapshot()
	state.Agent.RunID = request.RunID
	state.Result = &result
	state.Status = status
	state.Owner = ""
	state.LeaseExpiresAt = time.Time{}
	state.UpdatedAt = time.Now().UTC()
	if status == RunAborted && r.control != nil {
		if lifecycle, ok := r.control.(controlLifecycle); ok {
			_ = lifecycle.CompleteAbort(context.Background(), request.RunID, "run aborted")
		}
	}
	if status == RunAborted {
		abortRequested, _ := NewEventEnvelope(EventAbortRequested, request.RunID, 0, map[string]any{"status": status})
		if effectiveIdempotencyKey != "" {
			abortRequested.IdempotencyKey = effectiveIdempotencyKey + ":abort-requested"
		}
		appendEnvelope(abortRequested)
	}
	if runErr != nil {
		state.Result = &result
	}
	saveErr := r.saveState(context.Background(), request.RunID, expectedVersion, state)
	if saveErr == nil || errors.Is(saveErr, ErrStateNotFound) {
		state.Version = expectedVersion + 1
	} else if runErr == nil {
		runErr = saveErr
	}
	if r.transcriptStore != nil && saveErr == nil {
		if err := r.saveTranscript(context.Background(), request.RunID, state.Version, state.Agent.Transcript); err != nil && runErr == nil {
			runErr = err
		}
	}
	checkpoint, _ := NewEventEnvelope(EventStateCheckpointed, request.RunID, 0, map[string]any{
		"version": state.Version,
		"status":  state.Status,
	})
	if effectiveIdempotencyKey != "" {
		checkpoint.IdempotencyKey = effectiveIdempotencyKey + ":terminal-checkpoint"
	}
	appendEnvelope(checkpoint)
	if status != RunRecoverable && status != RunWaitingApproval {
		endKind := EventAgentCompleted
		if status == RunAborted {
			endKind = EventAbortCompleted
		}
		end, _ := NewEventEnvelope(endKind, request.RunID, 0, map[string]any{"status": status, "error": errorString(runErr)})
		if effectiveIdempotencyKey != "" {
			end.IdempotencyKey = effectiveIdempotencyKey + ":agent-end"
		}
		appendEnvelope(end)
	}
	eventsMu.Lock()
	acceptEvents = false
	durableEventErr := eventStoreErr
	eventsMu.Unlock()
	unsubscribe()
	if durableEventErr != nil && runErr == nil {
		runErr = durableEventErr
	}
	return RunResponse{RunID: request.RunID, Snapshot: state, Result: result, Events: events}, runErr
}

func (r *AgentRunner) loadInitial(ctx context.Context, request RunRequest) (Snapshot, uint64, error) {
	if request.InitialState != nil {
		state := normalizeSnapshot(*request.InitialState, request.RunID)
		var err error
		state, err = state.Migrate()
		if err != nil {
			return Snapshot{}, 0, err
		}
		if state.RunID != request.RunID {
			return Snapshot{}, 0, fmt.Errorf("%w: initial state run_id mismatch", ErrInvalidSnapshot)
		}
		if err := state.Validate(); err != nil {
			return Snapshot{}, 0, err
		}
		return state, state.Version, nil
	}
	state, err := r.loadState(ctx, request.RunID)
	if errors.Is(err, ErrStateNotFound) {
		return NewSnapshot(request.RunID, AgentSnapshot{SchemaVersion: CurrentSnapshotSchemaVersion, RunID: request.RunID}), 0, nil
	}
	if err != nil {
		return Snapshot{}, 0, err
	}
	state = normalizeSnapshot(state, request.RunID)
	state, err = state.Migrate()
	if err != nil {
		return Snapshot{}, 0, err
	}
	if err := r.hydrateTranscript(ctx, &state); err != nil {
		return Snapshot{}, 0, err
	}
	if err := state.Validate(); err != nil {
		return Snapshot{}, 0, err
	}
	return state, state.Version, nil
}

func (r *AgentRunner) hydrateTranscript(ctx context.Context, state *Snapshot) error {
	if r.transcriptStore == nil {
		return nil
	}
	messages, err := r.transcriptStore.LoadTranscript(ctx, state.RunID)
	if errors.Is(err, ErrTranscriptNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	state.Agent.Transcript = messages
	return nil
}

func (r *AgentRunner) monitorAbort(ctx context.Context, runID string, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(r.pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			control, err := r.control.GetControl(ctx, runID)
			if err != nil {
				continue
			}
			if control.Status == ControlRequested || control.Status == ControlObserved {
				if lifecycle, ok := r.control.(controlLifecycle); ok {
					_ = lifecycle.ObserveAbort(ctx, runID)
				}
				cancel()
				return
			}
		}
	}
}

func (r *AgentRunner) monitorLease(ctx context.Context, store LeaseStore, lease Lease, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := store.RenewLease(ctx, lease, 2*time.Minute); err != nil {
				cancel()
				return
			}
		}
	}
}

func (r *AgentRunner) lockFor(runID string) *sync.Mutex {
	r.runLocksMu.Lock()
	defer r.runLocksMu.Unlock()
	lock := r.runLocks[runID]
	if lock == nil {
		lock = &sync.Mutex{}
		r.runLocks[runID] = lock
	}
	return lock
}

func statusForResult(result RunResult, err error) RunStatus {
	if result.State == StateWaitingApproval || errors.Is(err, ErrApprovalPending) {
		return RunWaitingApproval
	}
	if result.State == StateCanceled || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return RunAborted
	}
	if err != nil {
		if result.State == StateFailed && isRecoverable(err) {
			return RunRecoverable
		}
		return RunFailed
	}
	return RunCompleted
}

func promptMessage(prompt string) ai.Message {
	if prompt == "" {
		return ai.Message{}
	}
	return ai.Message{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: prompt}}, Timestamp: time.Now().UTC()}
}

func envelopeFromEvent(event Event, runID string, sequence uint64) (EventEnvelope, error) {
	eventType := event.Kind
	switch eventType {
	case EventTextDelta:
		eventType = EventMessageDelta
	case EventTurnEnded:
		eventType = EventTurnCompleted
	case EventToolEnded:
		eventType = EventToolCompleted
	case EventCompleted:
		eventType = EventAgentCompleted
	}
	toolCall := event.ToolCall
	toolCall.Arguments = policy.Redact(toolCall.Arguments)
	toolResult := redactedToolResult(event.ToolResult)
	message := cloneMessage(event.Message)
	for i := range message.ToolCalls {
		message.ToolCalls[i].Arguments = policy.Redact(message.ToolCalls[i].Arguments)
	}
	payload := map[string]any{
		"state": event.State, "turn": event.Turn, "message": message,
		"tool_call": toolCall, "tool_result": toolResult, "usage": event.Usage,
		"tool_progress": event.ToolProgress, "text_delta": event.TextDelta, "thinking_delta": event.ThinkingDelta,
	}
	if len(event.Payload) > 0 {
		payload["payload"] = json.RawMessage(append([]byte(nil), event.Payload...))
	}
	envelope, err := NewEventEnvelope(eventType, runID, sequence, payload)
	if err != nil {
		return EventEnvelope{}, err
	}
	envelope.SessionID = event.SessionID
	envelope.TurnID = event.TurnID
	envelope.ToolCallID = event.ToolCallID
	envelope.CorrelationID = event.CorrelationID
	envelope.IdempotencyKey = event.IdempotencyKey
	if event.Err != nil {
		envelope.Error = structuredEventError(event.Err)
	}
	return envelope, nil
}

func redactedToolResult(result ai.ToolResult) ai.ToolResult {
	raw, err := json.Marshal(result)
	if err != nil {
		return ai.ToolResult{}
	}
	redacted := policy.Redact(raw)
	if len(redacted) == 0 {
		return ai.ToolResult{}
	}
	if err := json.Unmarshal(redacted, &result); err != nil {
		return ai.ToolResult{}
	}
	return result
}

func structuredEventError(err error) *EventError {
	if err == nil {
		return nil
	}
	eventError := &EventError{Message: err.Error(), Retryable: isTransient(err)}
	var rateLimit *providers.RateLimitError
	var network *providers.NetworkError
	var authError *providers.AuthError
	var overflow *providers.ContextOverflowError
	switch {
	case errors.As(err, &rateLimit):
		eventError.Code = "rate_limit"
		eventError.Retryable = true
	case errors.As(err, &network):
		eventError.Code = "network"
		eventError.Retryable = network.StatusCode == 0 || network.StatusCode >= 500
	case errors.As(err, &authError):
		eventError.Code = "authentication"
		eventError.Retryable = false
	case errors.As(err, &overflow):
		eventError.Code = "context_overflow"
		eventError.Retryable = false
	case errors.Is(err, ErrRetry):
		eventError.Code = "retry_requested"
		eventError.Retryable = true
	}
	return eventError
}

// EventEnvelopeFromEvent converts the in-process event into the public wire
// envelope. The sequence is supplied by the owning runner/store.
func EventEnvelopeFromEvent(event Event, runID string, sequence uint64) (EventEnvelope, error) {
	return envelopeFromEvent(event, runID, sequence)
}

func cloneSnapshot(snapshot Snapshot) (Snapshot, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	var out Snapshot
	if err := json.Unmarshal(raw, &out); err != nil {
		return Snapshot{}, err
	}
	return out, nil
}

func cloneEvent(event EventEnvelope) EventEnvelope {
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	if event.Error != nil {
		copy := *event.Error
		event.Error = &copy
	}
	return event
}

func validateOperation(operation OperationRecord) error {
	if operation.SchemaVersion < 0 || operation.SchemaVersion > CurrentOperationSchemaVersion {
		return fmt.Errorf("%w: unsupported operation schema version %d", ErrInvalidEvent, operation.SchemaVersion)
	}
	if strings.TrimSpace(operation.IdempotencyKey) == "" {
		return fmt.Errorf("%w: operation idempotency key is required", ErrInvalidEvent)
	}
	if strings.TrimSpace(operation.ToolName) == "" {
		return fmt.Errorf("%w: operation tool name is required", ErrInvalidEvent)
	}
	if operation.State != "" && operation.State != OperationPending && operation.State != OperationCompleted && operation.State != OperationFailed {
		return fmt.Errorf("%w: invalid operation state %q", ErrInvalidEvent, operation.State)
	}
	return nil
}

func compareOperationIdentity(existing, requested OperationRecord) error {
	if existing.ArgumentsHash != requested.ArgumentsHash || existing.RunID != requested.RunID ||
		existing.TurnID != requested.TurnID || existing.ToolCallID != requested.ToolCallID ||
		existing.ToolName != requested.ToolName {
		return fmt.Errorf("%w: key %q", ErrIdempotencyConflict, requested.IdempotencyKey)
	}
	return nil
}

func cloneOperation(operation OperationRecord) OperationRecord {
	operation.Response = cloneToolResponsePtrValue(operation.Response)
	if operation.Error != nil {
		copy := *operation.Error
		operation.Error = &copy
	}
	return operation
}

func cloneToolResponsePtr(response tools.ToolResponse) *tools.ToolResponse {
	copy := cloneToolResponse(response)
	return &copy
}

func cloneToolResponsePtrValue(response *tools.ToolResponse) *tools.ToolResponse {
	if response == nil {
		return nil
	}
	return cloneToolResponsePtr(*response)
}

func cloneToolResponse(response tools.ToolResponse) tools.ToolResponse {
	copy := tools.ToolResponse{
		IsError:  response.IsError,
		Details:  append([]byte(nil), response.Details...),
		Metadata: append([]byte(nil), response.Metadata...),
		Logs:     append([]tools.LogEntry(nil), response.Logs...),
	}
	for i := range copy.Logs {
		copy.Logs[i].Details = append([]byte(nil), copy.Logs[i].Details...)
	}
	for _, block := range response.Content {
		copy.Content = append(copy.Content, tools.ContentBlock{
			Type:             block.Type,
			Text:             block.Text,
			ImageData:        append([]byte(nil), block.ImageData...),
			ImageMIMEType:    block.ImageMIMEType,
			Details:          append([]byte(nil), block.Details...),
			ProviderMetadata: append([]byte(nil), block.ProviderMetadata...),
		})
	}
	return copy
}

func structuredOperationError(err error) *EventError {
	if err == nil {
		return nil
	}
	return &EventError{Code: "operation_error", Message: err.Error(), Retryable: isTransient(err)}
}

func hasKey(events []EventEnvelope, key string) bool {
	for _, event := range events {
		if event.IdempotencyKey == key {
			return true
		}
	}
	return false
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("%d", time.Now().UTC().UnixNano())
}
