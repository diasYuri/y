package component

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Scope is the mediated view a component receives during its lifecycle. Its
// fields are private; component state and service bindings cannot be changed
// by mutating Runtime internals.
type Scope struct {
	runtime    *Runtime
	descriptor Descriptor
	bindings   map[string]serviceBinding
	generation uint64
	mu         sync.Mutex
	cleanups   []Cleanup
	workerErr  error
	ctx        context.Context
	cancel     context.CancelFunc
	workers    sync.WaitGroup
	closeDone  chan struct{}
	accepting  bool
	closing    bool
	closed     bool
	closeErr   error
}

// Owner returns the stable component ID that owns this scope.
func (s *Scope) Owner() string {
	if s == nil {
		return ""
	}
	return s.descriptor.ID
}

// Generation returns the component generation that owns this scope.
func (s *Scope) Generation() uint64 {
	if s == nil {
		return 0
	}
	return s.generation
}

// Track records cleanup for a resource or registration acquired during Start.
// Cleanups run in reverse registration order, including when Start fails, and
// every cleanup is attempted if an earlier cleanup returns an error. Track
// releases managed acquisitions; it does not erase durable data or compensate
// for externally visible operations such as sending a message or calling an API.
func (s *Scope) Track(cleanup Cleanup) error {
	if s == nil || cleanup == nil {
		return errors.New("component: scope and cleanup are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.accepting {
		return ErrScopeClosed
	}
	s.cleanups = append(s.cleanups, cleanup)
	return nil
}

// Go starts a scope-owned worker during component startup. Workers receive a
// context canceled when the scope begins closing. Close waits for every worker
// to return before releasing tracked resources. A worker error is joined into
// the scope close result; context cancellation during normal shutdown is ignored.
func (s *Scope) Go(worker func(context.Context) error) error {
	if s == nil || worker == nil {
		return errors.New("component: scope and worker are required")
	}
	s.mu.Lock()
	if s.closed || !s.accepting || s.ctx == nil {
		s.mu.Unlock()
		return ErrScopeClosed
	}
	s.workers.Add(1)
	ctx := s.ctx
	s.mu.Unlock()

	go func() {
		defer s.workers.Done()
		if err := worker(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.mu.Lock()
			s.workerErr = errors.Join(s.workerErr, err)
			s.mu.Unlock()
		}
	}()
	return nil
}

// Require resolves a service only if the component declared it in
// Descriptor.Requires or Descriptor.Requirements.
func Require[T any](scope *Scope, key Key[T]) (T, error) {
	var zero T
	if scope == nil || scope.runtime == nil || !validRef(key.ref) {
		return zero, fmt.Errorf("%w: invalid scope or service key", ErrServiceUnavailable)
	}
	requirementDeclared := containsRef(scope.descriptor.Requires, key.ref)
	var constraint VersionConstraint
	var selectedProvider string
	for _, requirement := range scope.descriptor.Requirements {
		if requirement.ServiceID == key.ref.ID {
			requirementDeclared = true
			constraint = requirement.Version
			selectedProvider = requirement.ProviderID
			break
		}
	}
	if !requirementDeclared {
		return zero, fmt.Errorf("%w: component %q requires %q@%s", ErrUndeclaredService, scope.descriptor.ID, key.ref.ID, key.ref.Version)
	}
	scope.mu.Lock()
	closed := scope.closed
	scope.mu.Unlock()
	if closed {
		return zero, ErrScopeClosed
	}
	binding, bound := scope.bindings[key.ref.ID]
	if !bound || (selectedProvider != "" && binding.providerID != selectedProvider) {
		return zero, fmt.Errorf("%w: component %q has no binding for %q", ErrServiceUnavailable, scope.descriptor.ID, key.ref.ID)
	}
	if selectedProvider != "" && !constraint.Matches(key.ref.Version) {
		return zero, fmt.Errorf("%w: component %q key version %q is outside %s", ErrVersionMismatch, scope.descriptor.ID, key.ref.Version, describeConstraint(constraint))
	}
	scope.runtime.mu.RLock()
	value, exists := scope.runtime.services[binding]
	scope.runtime.mu.RUnlock()
	if !exists || (selectedProvider == "" && value.version != key.ref.Version) ||
		(selectedProvider != "" && !constraint.Matches(value.version)) {
		return zero, fmt.Errorf("%w: component %q requires %q@%s", ErrServiceUnavailable, scope.descriptor.ID, key.ref.ID, key.ref.Version)
	}
	return unbox[T](value.value, key.ref)
}

// Provide publishes a service only if the component declared the exact
// service reference in Descriptor.Provides. A component must install every
// declared provision before Start returns nil.
func Provide[T any](scope *Scope, key Key[T], value T) error {
	if scope == nil || scope.runtime == nil || !validRef(key.ref) {
		return errors.New("component: scope and valid service key are required")
	}
	if !containsRef(scope.descriptor.Provides, key.ref) {
		return fmt.Errorf("%w: component %q provides %q@%s", ErrUndeclaredService, scope.descriptor.ID, key.ref.ID, key.ref.Version)
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.closed || !scope.accepting {
		return ErrScopeClosed
	}
	scope.runtime.mu.Lock()
	defer scope.runtime.mu.Unlock()
	binding := serviceBinding{serviceID: key.ref.ID, providerID: scope.descriptor.ID, generation: scope.generation}
	if _, exists := scope.runtime.services[binding]; exists {
		return fmt.Errorf("%w: %q from %q", ErrDuplicateProvider, key.ref.ID, scope.descriptor.ID)
	}
	scope.runtime.services[binding] = serviceValue{
		owner:      scope.descriptor.ID,
		serviceID:  key.ref.ID,
		generation: scope.generation,
		version:    key.ref.Version,
		value:      serviceBox[T]{value: value},
	}
	return nil
}

func (s *Scope) seal() {
	s.mu.Lock()
	s.accepting = false
	s.mu.Unlock()
}

func (s *Scope) missingProvisions() []string {
	missing := make([]string, 0)
	s.runtime.mu.RLock()
	defer s.runtime.mu.RUnlock()
	for _, provided := range s.descriptor.Provides {
		binding := serviceBinding{serviceID: provided.ID, providerID: s.descriptor.ID, generation: s.generation}
		value, exists := s.runtime.services[binding]
		if !exists || value.owner != s.descriptor.ID || value.version != provided.Version {
			missing = append(missing, provided.ID+"@"+provided.Version)
		}
	}
	return missing
}

func (s *Scope) close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		s.mu.Unlock()
		return err
	}
	if s.closing {
		done := s.closeDone
		s.mu.Unlock()
		select {
		case <-done:
			s.mu.Lock()
			err := s.closeErr
			s.mu.Unlock()
			return err
		default:
		}
		select {
		case <-done:
			s.mu.Lock()
			err := s.closeErr
			s.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.closing = true
	s.accepting = false
	if s.closeDone == nil {
		s.closeDone = make(chan struct{})
	}
	done := s.closeDone
	cleanups := append([]Cleanup(nil), s.cleanups...)
	s.cleanups = nil
	cancel := s.cancel
	s.mu.Unlock()

	cleanupCtx := context.WithoutCancel(ctx)
	go func() {
		if cancel != nil {
			cancel()
		}
		s.workers.Wait()
		s.mu.Lock()
		result := s.workerErr
		s.mu.Unlock()
		for i := len(cleanups) - 1; i >= 0; i-- {
			if err := cleanups[i](cleanupCtx); err != nil {
				result = errors.Join(result, err)
			}
		}
		if s.runtime != nil {
			s.runtime.mu.Lock()
			for _, provided := range s.descriptor.Provides {
				binding := serviceBinding{serviceID: provided.ID, providerID: s.descriptor.ID, generation: s.generation}
				if value, exists := s.runtime.services[binding]; exists && value.owner == s.descriptor.ID {
					delete(s.runtime.services, binding)
				}
			}
			s.runtime.mu.Unlock()
		}
		s.mu.Lock()
		s.closing = false
		s.closed = true
		s.closeErr = result
		close(done)
		s.mu.Unlock()
	}()

	select {
	case <-done:
		s.mu.Lock()
		err := s.closeErr
		s.mu.Unlock()
		return err
	default:
	}
	select {
	case <-done:
		s.mu.Lock()
		err := s.closeErr
		s.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func containsRef(refs []ServiceRef, target ServiceRef) bool {
	for _, ref := range refs {
		if ref == target {
			return true
		}
	}
	return false
}
