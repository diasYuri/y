package component

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
)

// ComponentState is the observed state of a desired component instance.
type ComponentState string

const (
	ComponentInactive ComponentState = "inactive"
	ComponentStarting ComponentState = "starting"
	ComponentActive   ComponentState = "active"
	ComponentStopping ComponentState = "stopping"
	ComponentFailed   ComponentState = "failed"
	ComponentClosed   ComponentState = "closed"
)

// ComponentStatus describes the committed state of one logical component ID.
type ComponentStatus struct {
	ID         string
	State      ComponentState
	Generation uint64
	Missing    []string
	Err        error
}

// ReconcileError reports whether a failed reconciliation had already committed
// its desired view. Cleanup errors happen after commit; validation and startup
// failures leave the previous committed view active.
type ReconcileError struct {
	Committed bool
	Err       error
}

func (e *ReconcileError) Error() string {
	if e == nil || e.Err == nil {
		return "component: reconciliation failed"
	}
	return e.Err.Error()
}

func (e *ReconcileError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type managedInstance struct {
	entry      runtimeEntry
	instance   Component
	runtime    *Runtime
	scope      *Scope
	generation uint64
	bindings   map[string]serviceBinding
	leaseMu    sync.Mutex
	leases     uint64
	retired    bool
	drained    chan struct{}
	drainedSet bool
}

type reconcileCompletion struct {
	done chan struct{}
	err  error
}

// Manager reconciles a desired component set into a committed active set.
// Reconcile calls are serialized; components with missing requirements remain
// desired but inactive until a later reconciliation supplies their providers.
type Manager struct {
	reconcileMu sync.Mutex
	mu          sync.RWMutex
	desired     map[string]Definition
	active      map[string]*managedInstance
	services    map[serviceBinding]serviceValue
	generations map[string]uint64
	statuses    map[string]ComponentStatus
	instanceID  string
	viewVersion uint64
	closeWanted bool
	closeDone   chan struct{}
	closeErr    error
}

// NewManager creates an empty dynamic component manager.
func NewManager() *Manager {
	return &Manager{
		desired:     make(map[string]Definition),
		active:      make(map[string]*managedInstance),
		services:    make(map[serviceBinding]serviceValue),
		generations: make(map[string]uint64),
		statuses:    make(map[string]ComponentStatus),
		instanceID:  newManagerInstanceID(),
	}
}

func newManagerInstanceID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("manager-%d", managerFallbackID.Add(1))
}

var managerFallbackID atomic.Uint64

// Reconcile atomically replaces the desired definition set. A missing required
// provider leaves that component inactive. If an eligible component fails to
// start, staged instances are closed and the previous committed set is kept.
// Changes are intended to be applied between agent turns; operation snapshots
// are provided by a later runtime-view contract.
func (m *Manager) Reconcile(ctx context.Context, definitions []Definition) error {
	if m == nil {
		return errors.New("component: manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.reconcileMu.Lock()
	unlock := true
	defer func() {
		if unlock {
			m.reconcileMu.Unlock()
		}
	}()

	m.mu.RLock()
	closing := m.closeWanted
	current := cloneInstances(m.active)
	m.mu.RUnlock()
	if closing {
		return ErrManagerClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	graph, err := compileGraph(definitions, graphOptions{allowMissing: true, allowMultipleProviders: true})
	if err != nil {
		return err
	}
	desired := definitionMap(definitions)
	view := &Runtime{state: StateStarting, services: make(map[serviceBinding]serviceValue)}
	target := make(map[string]*managedInstance, len(graph.ordered))
	newInstances := make([]*managedInstance, 0)
	providerTargets := make(map[string]*managedInstance, len(graph.ordered))
	newStatus := make(map[string]ComponentStatus, len(graph.ordered))

	for _, node := range graph.ordered {
		id := node.entry.descriptor.ID
		if err := ctx.Err(); err != nil {
			return m.rollbackReconcile(ctx, id, err, newInstances, current)
		}
		if !node.eligible {
			newStatus[id] = ComponentStatus{ID: id, State: ComponentInactive, Missing: append([]string(nil), node.missing...)}
			continue
		}
		bindings := make(map[string]serviceBinding, len(node.entry.bindings))
		providerUnavailable := ""
		for serviceID, providerID := range node.entry.bindings {
			provider := providerTargets[providerID]
			if provider == nil {
				providerUnavailable = "provider " + providerID + " is inactive"
				break
			}
			bindings[serviceID] = serviceBinding{serviceID: serviceID, providerID: providerID, generation: provider.generation}
		}
		if providerUnavailable != "" {
			newStatus[id] = ComponentStatus{ID: id, State: ComponentInactive, Missing: []string{providerUnavailable}}
			continue
		}

		old := current[id]
		if old != nil && sameManagedDefinition(old.entry.descriptor, node.entry.descriptor) && sameBindings(old.bindings, bindings) {
			target[id] = old
			providerTargets[id] = old
			copyInstanceServices(view, old)
			newStatus[id] = ComponentStatus{ID: id, State: ComponentActive, Generation: old.generation}
			continue
		}

		generation := m.nextGeneration(id)
		newStatus[id] = ComponentStatus{ID: id, State: ComponentStarting, Generation: generation}
		instance, startErr := startManagedInstance(ctx, view, node.entry, generation, bindings)
		if startErr != nil {
			return m.rollbackReconcile(ctx, id, startErr, newInstances, current)
		}
		target[id] = instance
		providerTargets[id] = instance
		newInstances = append(newInstances, instance)
		newStatus[id] = ComponentStatus{ID: id, State: ComponentActive, Generation: generation}
	}
	if err := ctx.Err(); err != nil {
		return m.rollbackReconcile(ctx, "", err, newInstances, current)
	}

	m.mu.Lock()
	if m.closeWanted {
		m.mu.Unlock()
		return m.rollbackReconcile(ctx, "", ErrManagerClosed, newInstances, current)
	}
	retired := retiredInstances(current, target)
	for _, instance := range retired {
		instance.markRetired()
	}
	m.active = target
	m.desired = desired
	m.services = collectInstanceServices(target)
	m.viewVersion++
	view.state = StateRunning
	for id, status := range newStatus {
		m.statuses[id] = status
	}
	for id := range current {
		if target[id] == nil {
			m.statuses[id] = ComponentStatus{ID: id, State: ComponentStopping, Generation: current[id].generation}
		}
	}
	if len(retired) == 0 {
		m.mu.Unlock()
		return nil
	}
	completion := &reconcileCompletion{done: make(chan struct{})}
	m.mu.Unlock()

	// Keep reconciliation serialized until retired dependents have released their
	// resources. Unlocking from this goroutine lets a caller timeout without
	// allowing another graph change to close a provider still used by teardown.
	unlock = false
	cleanupCtx := context.WithoutCancel(ctx)
	go func() {
		defer m.reconcileMu.Unlock()
		completion.err = closeManagedInstances(cleanupCtx, retired)
		m.mu.Lock()
		for _, old := range retired {
			if target[old.entry.descriptor.ID] == nil {
				if status, desired := newStatus[old.entry.descriptor.ID]; desired {
					m.statuses[old.entry.descriptor.ID] = status
					continue
				}
				m.statuses[old.entry.descriptor.ID] = ComponentStatus{
					ID: old.entry.descriptor.ID, State: ComponentClosed, Generation: old.generation,
				}
			}
		}
		close(completion.done)
		m.mu.Unlock()
	}()
	return waitForRetire(ctx, completion)
}

func (m *Manager) rollbackReconcile(ctx context.Context, failedID string, cause error, staged []*managedInstance, current map[string]*managedInstance) error {
	cleanupCtx := context.WithoutCancel(ctx)
	var cleanupErr error
	for i := len(staged) - 1; i >= 0; i-- {
		if err := staged[i].scope.close(cleanupCtx); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("rollback component %q: %w", staged[i].entry.descriptor.ID, err))
		}
	}
	err := errors.Join(cause, cleanupErr)
	if failedID != "" {
		m.mu.Lock()
		if old := current[failedID]; old != nil {
			m.statuses[failedID] = ComponentStatus{ID: failedID, State: ComponentActive, Generation: old.generation, Err: err}
		} else {
			m.statuses[failedID] = ComponentStatus{ID: failedID, State: ComponentFailed, Err: err}
		}
		m.mu.Unlock()
	}
	return &ReconcileError{Committed: false, Err: err}
}

func startManagedInstance(ctx context.Context, view *Runtime, entry runtimeEntry, generation uint64, bindings map[string]serviceBinding) (*managedInstance, error) {
	instance, err := entry.factory()
	if err != nil {
		return nil, fmt.Errorf("component %q factory: %w", entry.descriptor.ID, err)
	}
	if instance == nil {
		return nil, fmt.Errorf("%w: component %q factory returned nil", ErrInvalidFactory, entry.descriptor.ID)
	}
	scopeCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	scope := &Scope{
		runtime:    view,
		descriptor: entry.descriptor,
		bindings:   bindings,
		generation: generation,
		accepting:  true,
		ctx:        scopeCtx,
		cancel:     cancel,
	}
	if err := instance.Start(ctx, scope); err != nil {
		cleanupErr := scope.close(context.WithoutCancel(ctx))
		return nil, errors.Join(fmt.Errorf("component %q start: %w", entry.descriptor.ID, err), cleanupErr)
	}
	if err := ctx.Err(); err != nil {
		cleanupErr := scope.close(context.WithoutCancel(ctx))
		return nil, errors.Join(err, cleanupErr)
	}
	if missing := scope.missingProvisions(); len(missing) != 0 {
		cleanupErr := scope.close(context.WithoutCancel(ctx))
		return nil, errors.Join(fmt.Errorf("%w: component %q did not provide %v", ErrMissingProvision, entry.descriptor.ID, missing), cleanupErr)
	}
	scope.seal()
	return &managedInstance{entry: entry, instance: instance, runtime: view, scope: scope, generation: generation, bindings: cloneBindings(bindings), drained: make(chan struct{})}, nil
}

func (instance *managedInstance) acquireSnapshot() bool {
	instance.leaseMu.Lock()
	defer instance.leaseMu.Unlock()
	if instance.retired {
		return false
	}
	instance.leases++
	return true
}

func (instance *managedInstance) releaseSnapshot() {
	instance.leaseMu.Lock()
	defer instance.leaseMu.Unlock()
	if instance.leases > 0 {
		instance.leases--
	}
	if instance.retired && instance.leases == 0 && !instance.drainedSet {
		close(instance.drained)
		instance.drainedSet = true
	}
}

func (instance *managedInstance) markRetired() {
	instance.leaseMu.Lock()
	defer instance.leaseMu.Unlock()
	instance.retired = true
	if instance.leases == 0 && !instance.drainedSet {
		close(instance.drained)
		instance.drainedSet = true
	}
}

func (instance *managedInstance) waitForSnapshots(ctx context.Context) error {
	select {
	case <-instance.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) nextGeneration(id string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generations[id]++
	return m.generations[id]
}

func sameManagedDefinition(left, right Descriptor) bool {
	if left.Revision == "" {
		left.Revision = left.Version
	}
	if right.Revision == "" {
		right.Revision = right.Version
	}
	return reflect.DeepEqual(left, right)
}

func sameBindings(left, right map[string]serviceBinding) bool {
	return reflect.DeepEqual(left, right)
}

func cloneBindings(bindings map[string]serviceBinding) map[string]serviceBinding {
	copy := make(map[string]serviceBinding, len(bindings))
	for serviceID, binding := range bindings {
		copy[serviceID] = binding
	}
	return copy
}

func cloneInstances(instances map[string]*managedInstance) map[string]*managedInstance {
	copy := make(map[string]*managedInstance, len(instances))
	for id, instance := range instances {
		copy[id] = instance
	}
	return copy
}

func copyInstanceServices(view *Runtime, instance *managedInstance) {
	instance.runtime.mu.RLock()
	defer instance.runtime.mu.RUnlock()
	view.mu.Lock()
	defer view.mu.Unlock()
	for _, provided := range instance.entry.descriptor.Provides {
		binding := serviceBinding{serviceID: provided.ID, providerID: instance.entry.descriptor.ID, generation: instance.generation}
		if value, exists := instance.runtime.services[binding]; exists {
			view.services[binding] = value
		}
	}
}

func collectInstanceServices(instances map[string]*managedInstance) map[serviceBinding]serviceValue {
	services := make(map[serviceBinding]serviceValue)
	for _, instance := range instances {
		instance.runtime.mu.RLock()
		for _, provided := range instance.entry.descriptor.Provides {
			binding := serviceBinding{serviceID: provided.ID, providerID: instance.entry.descriptor.ID, generation: instance.generation}
			if value, exists := instance.runtime.services[binding]; exists {
				services[binding] = value
			}
		}
		instance.runtime.mu.RUnlock()
	}
	return services
}

func retiredInstances(current, target map[string]*managedInstance) []*managedInstance {
	retired := make(map[string]*managedInstance)
	for id, instance := range current {
		if target[id] != instance {
			retired[id] = instance
		}
	}
	if len(retired) < 2 {
		out := make([]*managedInstance, 0, len(retired))
		for _, instance := range retired {
			out = append(out, instance)
		}
		return out
	}
	indegree := make(map[string]int, len(retired))
	adjacency := make(map[string][]string, len(retired))
	for id := range retired {
		indegree[id] = 0
	}
	for consumerID, instance := range retired {
		for _, binding := range instance.bindings {
			if retired[binding.providerID] != nil {
				adjacency[binding.providerID] = append(adjacency[binding.providerID], consumerID)
				indegree[consumerID]++
			}
		}
	}
	ready := make([]string, 0, len(retired))
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	ordered := make([]*managedInstance, 0, len(retired))
	for len(ready) != 0 {
		id := ready[0]
		ready = ready[1:]
		ordered = append(ordered, retired[id])
		for _, consumer := range adjacency[id] {
			indegree[consumer]--
			if indegree[consumer] == 0 {
				ready = append(ready, consumer)
				sort.Strings(ready)
			}
		}
	}
	for left, right := 0, len(ordered)-1; left < right; left, right = left+1, right-1 {
		ordered[left], ordered[right] = ordered[right], ordered[left]
	}
	return ordered
}

func closeManagedInstances(ctx context.Context, instances []*managedInstance) error {
	var result error
	for _, instance := range instances {
		if err := instance.waitForSnapshots(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("wait for component %q generation %d snapshots: %w", instance.entry.descriptor.ID, instance.generation, err))
			return result
		}
		if err := instance.scope.close(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("close component %q generation %d: %w", instance.entry.descriptor.ID, instance.generation, err))
		}
	}
	return result
}

// Status returns a copy of one component's current lifecycle status.
func (m *Manager) Status(id string) (ComponentStatus, bool) {
	if m == nil {
		return ComponentStatus{}, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	status, exists := m.statuses[id]
	status.Missing = append([]string(nil), status.Missing...)
	return status, exists
}

// Statuses returns all current statuses in component ID order.
func (m *Manager) Statuses() []ComponentStatus {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.statuses))
	for id := range m.statuses {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]ComponentStatus, 0, len(ids))
	for _, id := range ids {
		status := m.statuses[id]
		status.Missing = append([]string(nil), status.Missing...)
		out = append(out, status)
	}
	return out
}

// ResolveService resolves a service by its explicitly selected provider. When
// constraint is omitted, the key's interface version must match exactly.
func ResolveService[T any](manager *Manager, providerID string, key Key[T], constraint ...VersionConstraint) (T, error) {
	var zero T
	if manager == nil || providerID == "" || !validRef(key.ref) {
		return zero, fmt.Errorf("%w: invalid manager, provider, or service key", ErrServiceUnavailable)
	}
	if len(constraint) > 1 {
		return zero, errors.New("component: at most one service version constraint is allowed")
	}
	if len(constraint) == 1 {
		if err := constraint[0].Validate(); err != nil {
			return zero, err
		}
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	bindingValue, exists := manager.active[providerID]
	if !exists {
		return zero, fmt.Errorf("%w: provider %q is not active", ErrServiceUnavailable, providerID)
	}
	binding := serviceBinding{serviceID: key.ref.ID, providerID: providerID, generation: bindingValue.generation}
	value, exists := manager.services[binding]
	compatible := exists && value.version == key.ref.Version
	if exists && len(constraint) == 1 {
		compatible = constraint[0].Matches(value.version)
	}
	if !compatible {
		return zero, fmt.Errorf("%w: provider %q does not provide %q@%s", ErrServiceUnavailable, providerID, key.ref.ID, key.ref.Version)
	}
	return unbox[T](value.value, key.ref)
}

// Close shuts down the active set in reverse dependency order. A timeout limits
// caller waiting; all scopes continue closing and concurrent calls share result.
func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.closeDone != nil {
		done := m.closeDone
		m.mu.Unlock()
		return waitForManagerClose(ctx, done, m)
	}
	m.closeWanted = true
	done := make(chan struct{})
	m.closeDone = done
	m.mu.Unlock()
	go func() {
		m.reconcileMu.Lock()
		defer m.reconcileMu.Unlock()
		m.mu.Lock()
		active := cloneInstances(m.active)
		m.active = make(map[string]*managedInstance)
		m.services = make(map[serviceBinding]serviceValue)
		for id, instance := range active {
			instance.markRetired()
			m.statuses[id] = ComponentStatus{ID: id, State: ComponentStopping, Generation: instance.generation}
		}
		m.mu.Unlock()
		ordered := retiredInstances(active, nil)
		closeErr := closeManagedInstances(context.Background(), ordered)
		m.mu.Lock()
		for id := range active {
			m.statuses[id] = ComponentStatus{ID: id, State: ComponentClosed, Generation: active[id].generation}
		}
		m.closeErr = closeErr
		close(done)
		m.mu.Unlock()
	}()
	return waitForManagerClose(ctx, done, m)
}

func waitForRetire(ctx context.Context, completion *reconcileCompletion) error {
	select {
	case <-completion.done:
		if completion.err != nil {
			return &ReconcileError{Committed: true, Err: completion.err}
		}
		return nil
	default:
	}
	select {
	case <-completion.done:
		if completion.err != nil {
			return &ReconcileError{Committed: true, Err: completion.err}
		}
		return nil
	case <-ctx.Done():
		return &ReconcileError{Committed: true, Err: ctx.Err()}
	}
}

func waitForManagerClose(ctx context.Context, done <-chan struct{}, manager *Manager) error {
	select {
	case <-done:
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		return manager.closeErr
	default:
	}
	select {
	case <-done:
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		return manager.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func definitionMap(definitions []Definition) map[string]Definition {
	result := make(map[string]Definition, len(definitions))
	for _, definition := range definitions {
		definition.Descriptor = cloneDescriptor(definition.Descriptor)
		if definition.Descriptor.Revision == "" {
			definition.Descriptor.Revision = definition.Descriptor.Version
		}
		result[definition.Descriptor.ID] = definition
	}
	return result
}
