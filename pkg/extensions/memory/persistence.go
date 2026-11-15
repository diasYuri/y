package memory

import (
	"context"
	"errors"
	"sync"
	"time"

	ycontext "github.com/diasYuri/y/pkg/context"
	pmemory "github.com/diasYuri/y/pkg/memory"
)

const defaultReadinessTimeout = 5 * time.Second

// PersistenceFailurePolicy controls what the memory component does when its
// configured durable backend is unavailable during startup.
type PersistenceFailurePolicy string

const (
	// PersistenceRequired prevents activation when the durable backend cannot
	// pass its readiness check.
	PersistenceRequired PersistenceFailurePolicy = "required"
	// PersistenceDegraded allows activation while exposing unavailable storage
	// through PersistenceStatus and returning errors from failed writes.
	PersistenceDegraded PersistenceFailurePolicy = "degraded"
)

// PersistenceStatus reports the last known state of the configured durable
// memory backend.
type PersistenceStatus string

const (
	PersistenceStatusNotRequired PersistenceStatus = "not_required"
	PersistenceStatusAvailable   PersistenceStatus = "available"
	PersistenceStatusDegraded    PersistenceStatus = "degraded"
)

type persistenceMonitor struct {
	mu     sync.RWMutex
	status PersistenceStatus
}

func (m *persistenceMonitor) current() PersistenceStatus {
	if m == nil {
		return PersistenceStatusNotRequired
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *persistenceMonitor) record(err error) {
	if m == nil {
		return
	}
	if err != nil && !isPersistenceAvailabilityFailure(err) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status == PersistenceStatusNotRequired {
		return
	}
	if err != nil {
		m.status = PersistenceStatusDegraded
	} else {
		m.status = PersistenceStatusAvailable
	}
}

func isPersistenceAvailabilityFailure(err error) bool {
	if err == nil {
		return true
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, pmemory.ErrNotFound) && !errors.Is(err, pmemory.ErrAlreadyForgotten) &&
		!errors.Is(err, pmemory.ErrInvalidMemory) && !errors.Is(err, pmemory.ErrSensitiveContent)
}

// PersistenceStatus returns the last known persistence state. A degraded
// status remains observable even when the context source is configured to
// fail open on reads.
func (e *Extension) PersistenceStatus() PersistenceStatus {
	if e == nil {
		return PersistenceStatusNotRequired
	}
	return e.persistence.current()
}

// RefreshPersistenceStatus performs a read-only readiness check and updates
// the state returned by PersistenceStatus. A failed check is also returned to
// the caller.
func (e *Extension) RefreshPersistenceStatus(ctx context.Context) error {
	if e == nil || e.persistence == nil {
		return errors.New("memory extension is nil")
	}
	if e.persistence.current() == PersistenceStatusNotRequired {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	store, ok := e.store.(*observedStore)
	if !ok {
		return errors.New("memory persistence monitor is unavailable")
	}
	err := store.CheckReadiness(ctx)
	e.persistence.record(err)
	return err
}

func requiresDurablePersistence(config Config) bool {
	return config.Enabled && config.Profile != "stateless" && config.Profile != "ephemeral" &&
		config.Backend != "noop" && config.Backend != "memory"
}

func checkStoreReadiness(ctx context.Context, store pmemory.Store) error {
	if store == nil {
		return pmemory.ErrPersistenceUnavailable
	}
	if checker, ok := store.(pmemory.ReadinessChecker); ok {
		return checker.CheckReadiness(ctx)
	}
	_, err := store.Version(ctx, ycontext.Request{})
	return err
}

func isNoopStore(store pmemory.Store) bool {
	switch store.(type) {
	case NoopStore, *NoopStore:
		return true
	default:
		return false
	}
}

type observedStore struct {
	store       pmemory.Store
	monitor     *persistenceMonitor
	unavailable error
}

func (s *observedStore) CheckReadiness(ctx context.Context) error {
	err := checkStoreReadiness(ctx, s.store)
	s.monitor.record(err)
	return err
}

func (s *observedStore) Search(ctx context.Context, query pmemory.Query) ([]pmemory.Memory, error) {
	if s.unavailable != nil {
		return nil, s.unavailable
	}
	values, err := s.store.Search(ctx, query)
	s.monitor.record(err)
	return values, err
}

func (s *observedStore) Read(ctx context.Context, id string) (pmemory.Memory, error) {
	if s.unavailable != nil {
		return pmemory.Memory{}, s.unavailable
	}
	value, err := s.store.Read(ctx, id)
	s.monitor.record(err)
	return value, err
}

func (s *observedStore) Save(ctx context.Context, value pmemory.Memory) error {
	if s.unavailable != nil {
		return s.unavailable
	}
	err := s.store.Save(ctx, value)
	s.monitor.record(err)
	return err
}

func (s *observedStore) Forget(ctx context.Context, id string) error {
	if s.unavailable != nil {
		return s.unavailable
	}
	err := s.store.Forget(ctx, id)
	s.monitor.record(err)
	return err
}

func (s *observedStore) Version(ctx context.Context, request ycontext.Request) (string, error) {
	if s.unavailable != nil {
		return "unavailable", s.unavailable
	}
	version, err := s.store.Version(ctx, request)
	s.monitor.record(err)
	return version, err
}

// MarkSurfaced preserves the store's optional usage accounting capability.
func (s *observedStore) MarkSurfaced(ctx context.Context, ids []string) error {
	tracker, ok := s.store.(pmemory.UsageTracker)
	if !ok {
		return nil
	}
	err := tracker.MarkSurfaced(ctx, ids)
	s.monitor.record(err)
	return err
}

var _ pmemory.Store = (*observedStore)(nil)
var _ pmemory.ReadinessChecker = (*observedStore)(nil)
var _ pmemory.UsageTracker = (*observedStore)(nil)
