package component

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// State is the lifecycle state of a Runtime.
type State string

const (
	StateNew      State = "new"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateFailed   State = "failed"
	StateStopping State = "stopping"
	StateClosed   State = "closed"
)

type runtimeEntry struct {
	descriptor Descriptor
	factory    Factory
	bindings   map[string]string
}

type serviceBinding struct {
	serviceID  string
	providerID string
	generation uint64
}

type serviceValue struct {
	owner      string
	serviceID  string
	generation uint64
	version    string
	value      any
}

// Runtime validates and owns a static component composition. A Runtime is
// single-use: after Start succeeds, fails, or Close is called, it cannot be
// started again. Dynamic replacement belongs to a later runtime-generation
// contract.
type Runtime struct {
	mu        sync.RWMutex
	state     State
	startDone chan struct{}
	ordered   []runtimeEntry
	services  map[serviceBinding]serviceValue
	started   []*Scope
	closeDone chan struct{}
	closeErr  error
}

// NewRuntime validates descriptors and computes a deterministic dependency
// order. It does not invoke factories or start components.
func NewRuntime(definitions ...Definition) (*Runtime, error) {
	runtime := &Runtime{
		state:    StateNew,
		services: make(map[serviceBinding]serviceValue),
	}
	graph, err := compileGraph(definitions, graphOptions{})
	if err != nil {
		return nil, err
	}
	if len(graph.inactive) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrMissingService, strings.Join(graph.inactive[0].missing, ", "))
	}
	runtime.ordered = make([]runtimeEntry, 0, len(graph.ordered))
	for _, node := range graph.ordered {
		runtime.ordered = append(runtime.ordered, node.entry)
	}
	return runtime, nil
}

// State reports the runtime lifecycle state.
func (r *Runtime) State() State {
	if r == nil {
		return StateClosed
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

// Descriptors returns defensive copies in the order the runtime will start
// components. It is safe to call before activation.
func (r *Runtime) Descriptors() []Descriptor {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Descriptor, len(r.ordered))
	for i, entry := range r.ordered {
		out[i] = cloneDescriptor(entry.descriptor)
	}
	return out
}

// Start constructs and starts each component in dependency order. If a
// factory or Start fails, the failing component's tracked resources and all
// previously started components are closed in reverse order.
func (r *Runtime) Start(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("%w: runtime is nil", ErrRuntimeState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.state != StateNew {
		state := r.state
		r.mu.Unlock()
		return fmt.Errorf("%w: cannot start from %q", ErrRuntimeState, state)
	}
	r.state = StateStarting
	started := make(chan struct{})
	r.startDone = started
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		close(started)
		if r.startDone == started {
			r.startDone = nil
		}
		r.mu.Unlock()
	}()

	for _, entry := range r.ordered {
		if err := ctx.Err(); err != nil {
			return r.failStart(ctx, nil, entry.descriptor.ID, err)
		}
		instance, err := entry.factory()
		if err != nil {
			return r.failStart(ctx, nil, entry.descriptor.ID, fmt.Errorf("factory: %w", err))
		}
		if instance == nil {
			return r.failStart(ctx, nil, entry.descriptor.ID, fmt.Errorf("%w: factory returned nil component", ErrInvalidFactory))
		}
		scopeCtx, scopeCancel := context.WithCancel(context.WithoutCancel(ctx))
		bindings := make(map[string]serviceBinding, len(entry.bindings))
		for serviceID, providerID := range entry.bindings {
			bindings[serviceID] = serviceBinding{serviceID: serviceID, providerID: providerID, generation: 1}
		}
		scope := &Scope{runtime: r, descriptor: entry.descriptor, bindings: bindings, generation: 1, accepting: true, ctx: scopeCtx, cancel: scopeCancel}
		startErr := instance.Start(ctx, scope)
		if startErr == nil {
			if err := ctx.Err(); err != nil {
				startErr = err
			}
		}
		if startErr == nil {
			if missing := scope.missingProvisions(); len(missing) > 0 {
				startErr = fmt.Errorf("%w: component %q did not provide %v", ErrMissingProvision, entry.descriptor.ID, missing)
			}
		}
		if startErr != nil {
			return r.failStart(ctx, scope, entry.descriptor.ID, startErr)
		}
		scope.seal()
		r.mu.Lock()
		r.started = append(r.started, scope)
		r.mu.Unlock()
	}

	r.mu.Lock()
	r.state = StateRunning
	r.mu.Unlock()
	return nil
}

func (r *Runtime) failStart(ctx context.Context, failedScope *Scope, componentID string, cause error) error {
	var cleanupErr error
	cleanupCtx := context.WithoutCancel(ctx)
	if failedScope != nil {
		cleanupErr = errors.Join(cleanupErr, failedScope.close(cleanupCtx))
	}
	r.mu.Lock()
	started := append([]*Scope(nil), r.started...)
	r.started = nil
	r.mu.Unlock()
	cleanupErr = errors.Join(cleanupErr, closeScopes(cleanupCtx, started))
	r.mu.Lock()
	r.state = StateFailed
	r.closeErr = cleanupErr
	r.mu.Unlock()
	return errors.Join(fmt.Errorf("component: start %q: %w", componentID, cause), cleanupErr)
}

// Close shuts down started components in reverse dependency order. It is
// idempotent; every tracked cleanup is attempted even if an earlier one fails.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	switch r.state {
	case StateClosed:
		err := r.closeErr
		r.mu.Unlock()
		return err
	case StateNew:
		r.state = StateClosed
		r.mu.Unlock()
		return nil
	case StateFailed:
		r.state = StateClosed
		err := r.closeErr
		r.mu.Unlock()
		return err
	case StateRunning:
		r.state = StateStopping
		r.closeDone = make(chan struct{})
	default:
		if r.state == StateStarting && r.startDone != nil {
			started := r.startDone
			r.mu.Unlock()
			select {
			case <-started:
				return r.Close(ctx)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if r.state == StateStopping && r.closeDone != nil {
			done := r.closeDone
			r.mu.Unlock()
			return waitForClose(ctx, done, func() error {
				r.mu.RLock()
				defer r.mu.RUnlock()
				return r.closeErr
			})
		}
		state := r.state
		r.mu.Unlock()
		return fmt.Errorf("%w: cannot close from %q", ErrRuntimeState, state)
	}
	started := append([]*Scope(nil), r.started...)
	r.started = nil
	done := r.closeDone
	r.mu.Unlock()

	cleanupCtx := context.WithoutCancel(ctx)
	go func() {
		err := closeScopes(cleanupCtx, started)
		r.mu.Lock()
		r.state = StateClosed
		r.closeErr = err
		close(done)
		r.mu.Unlock()
	}()
	return waitForClose(ctx, done, func() error {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.closeErr
	})
}

func waitForClose(ctx context.Context, done <-chan struct{}, result func() error) error {
	select {
	case <-done:
		return result()
	default:
	}
	select {
	case <-done:
		return result()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func closeScopes(ctx context.Context, scopes []*Scope) error {
	var result error
	for i := len(scopes) - 1; i >= 0; i-- {
		if err := scopes[i].close(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("close component %q: %w", scopes[i].descriptor.ID, err))
		}
	}
	return result
}

// Resolve returns a running service using its typed key. Components should
// normally use Require with their Scope so their declared dependencies are
// enforced.
func Resolve[T any](runtime *Runtime, key Key[T]) (T, error) {
	var zero T
	if runtime == nil || !validRef(key.ref) {
		return zero, fmt.Errorf("%w: invalid runtime or service key", ErrServiceUnavailable)
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if runtime.state != StateRunning {
		return zero, fmt.Errorf("%w: runtime is %q", ErrServiceUnavailable, runtime.state)
	}
	var value serviceValue
	found := false
	for binding, candidate := range runtime.services {
		if binding.serviceID != key.ref.ID {
			continue
		}
		if found {
			return zero, fmt.Errorf("%w: %q", ErrAmbiguousService, key.ref.ID)
		}
		value = candidate
		found = true
	}
	if !found || value.version != key.ref.Version {
		return zero, fmt.Errorf("%w: %q@%s", ErrServiceUnavailable, key.ref.ID, key.ref.Version)
	}
	return unbox[T](value.value, key.ref)
}

// ResolveFrom returns a service from one explicitly selected provider instance.
func ResolveFrom[T any](runtime *Runtime, providerID string, key Key[T]) (T, error) {
	var zero T
	if runtime == nil || providerID == "" || !validRef(key.ref) {
		return zero, fmt.Errorf("%w: invalid runtime, provider, or service key", ErrServiceUnavailable)
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if runtime.state != StateRunning {
		return zero, fmt.Errorf("%w: runtime is %q", ErrServiceUnavailable, runtime.state)
	}
	for binding, value := range runtime.services {
		if binding.serviceID == key.ref.ID && binding.providerID == providerID && value.version == key.ref.Version {
			return unbox[T](value.value, key.ref)
		}
	}
	return zero, fmt.Errorf("%w: provider %q does not provide %q@%s", ErrServiceUnavailable, providerID, key.ref.ID, key.ref.Version)
}

func validRef(ref ServiceRef) bool { return ref.ID != "" && ref.Version != "" }

type serviceBox[T any] struct{ value T }

func unbox[T any](value any, ref ServiceRef) (T, error) {
	var zero T
	boxed, ok := value.(serviceBox[T])
	if !ok {
		return zero, fmt.Errorf("%w: %q@%s", ErrServiceType, ref.ID, ref.Version)
	}
	return boxed.value, nil
}
