package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/diasYuri/y/pkg/tools"
)

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
