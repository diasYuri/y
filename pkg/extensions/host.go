package extensions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/component"
	ycontext "github.com/diasYuri/y/pkg/context"
	"github.com/diasYuri/y/pkg/tools"
)

// Extension is a runtime extension that installs its own tools, context
// sources, and lifecycle hooks into a host. The host deliberately knows only
// how to compose these generic contracts; extension-specific configuration and
// implementation stay in the extension package.
type Extension interface {
	ID() string
	Install(*Host) error
	Close(context.Context) error
}

// ScopedInstaller is an optional extension interface. Implementations use the
// supplied scope for every runtime registration so a failed install rolls back
// partial contributions and a later close removes only those contributions.
type ScopedInstaller interface {
	InstallScoped(context.Context, *Host, *component.Scope) error
}

// ComponentDescriber optionally declares service requirements and provisions
// for an extension managed reactively by Host.
type ComponentDescriber interface {
	ComponentDescriptor() component.Descriptor
}

// ExtensionFactory creates a fresh extension instance for every runtime
// generation that the host activates.
type ExtensionFactory interface {
	NewExtension() (Extension, error)
}

// ExtensionDefinition binds stable extension metadata to a side-effect-free
// factory. The factory is called only when all selected service requirements
// are available.
type ExtensionDefinition struct {
	ID         string
	Descriptor component.Descriptor
	Factory    func() (Extension, error)
}

// Host composes optional extensions for one runtime instance.
type Host struct {
	Registry *tools.Registry

	mu           sync.Mutex
	installMu    sync.Mutex
	resolver     *ycontext.Resolver
	hookRegistry *agent.RuntimeHookRegistry
	agentOptions []agent.Option
	manager      *component.Manager
	extensions   map[string]ExtensionDefinition
	hostCleanups []component.Cleanup
	closed       bool
	closeDone    chan struct{}
	closeErr     error
}

// NewHost creates an extension host backed by registry.
func NewHost(registry *tools.Registry) *Host {
	return &Host{
		Registry:     registry,
		resolver:     ycontext.NewResolver(),
		hookRegistry: agent.NewRuntimeHookRegistry(),
		manager:      component.NewManager(),
		extensions:   make(map[string]ExtensionDefinition),
	}
}

// Install installs ext using a non-cancelable startup context. Call
// InstallContext when startup needs a deadline.
func (h *Host) Install(ext Extension) error {
	return h.InstallContext(context.Background(), ext)
}

// InstallContext starts an extension inside its own component scope. Scoped
// installers receive owner-aware registration helpers; legacy Install methods
// remain supported for host-lifetime composition.
func (h *Host) InstallContext(ctx context.Context, ext Extension) error {
	if h == nil {
		return errors.New("extension host is nil")
	}
	if ext == nil {
		return errors.New("extension is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id := ext.ID()
	if id == "" {
		return errors.New("extension ID is empty")
	}
	definition, err := extensionDefinition(ext)
	if err != nil {
		return err
	}
	h.installMu.Lock()
	defer h.installMu.Unlock()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errors.New("extension host is closed")
	}
	if _, exists := h.extensions[id]; exists {
		h.mu.Unlock()
		return fmt.Errorf("extension %q is already installed", id)
	}
	desired := cloneExtensionDefinitions(h.extensions)
	desired[id] = definition
	h.mu.Unlock()
	err = h.reconcileExtensionsLocked(ctx, desired)
	if err == nil || reconcileCommitted(err) {
		h.mu.Lock()
		h.extensions = desired
		h.mu.Unlock()
	}
	return err
}

// Reconcile atomically replaces the desired extension set. Extensions with
// unmet service requirements remain desired and inactive until a later call.
func (h *Host) Reconcile(ctx context.Context, desired []ExtensionDefinition) error {
	if h == nil {
		return errors.New("extension host is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	byID := make(map[string]ExtensionDefinition, len(desired))
	for _, definition := range desired {
		if definition.ID == "" || definition.Factory == nil {
			return errors.New("extension definition and non-empty ID and factory are required")
		}
		if definition.Descriptor.ID != "extensions/"+definition.ID {
			return fmt.Errorf("extension %q descriptor ID must be %q", definition.ID, "extensions/"+definition.ID)
		}
		if _, exists := byID[definition.ID]; exists {
			return fmt.Errorf("extension %q is listed more than once", definition.ID)
		}
		byID[definition.ID] = definition
	}
	h.installMu.Lock()
	defer h.installMu.Unlock()
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return errors.New("extension host is closed")
	}
	err := h.reconcileExtensionsLocked(ctx, byID)
	if err == nil || reconcileCommitted(err) {
		h.mu.Lock()
		h.extensions = byID
		h.mu.Unlock()
	}
	return err
}

// AddDefinitions adds or replaces definitions while preserving the other
// desired extensions. It is useful for independently configured component
// families such as WASM modules.
func (h *Host) AddDefinitions(ctx context.Context, additions []ExtensionDefinition) error {
	if h == nil {
		return errors.New("extension host is nil")
	}
	h.installMu.Lock()
	defer h.installMu.Unlock()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errors.New("extension host is closed")
	}
	desired := cloneExtensionDefinitions(h.extensions)
	h.mu.Unlock()
	for _, definition := range additions {
		if definition.ID == "" || definition.Factory == nil || definition.Descriptor.ID != "extensions/"+definition.ID {
			return errors.New("extension definitions require an ID, matching descriptor, and factory")
		}
		desired[definition.ID] = definition
	}
	err := h.reconcileExtensionsLocked(ctx, desired)
	if err == nil || reconcileCommitted(err) {
		h.mu.Lock()
		h.extensions = desired
		h.mu.Unlock()
	}
	return err
}

// AddCleanup registers a host-owned resource to close after component scopes
// have been drained. Cleanups run in reverse registration order.
func (h *Host) AddCleanup(cleanup component.Cleanup) error {
	if h == nil || cleanup == nil {
		return errors.New("extension host and cleanup are required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errors.New("extension host is closed")
	}
	h.hostCleanups = append(h.hostCleanups, cleanup)
	return nil
}

func (h *Host) reconcileExtensionsLocked(ctx context.Context, extensions map[string]ExtensionDefinition) error {
	ids := make([]string, 0, len(extensions))
	for id := range extensions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	definitions := make([]component.Definition, 0, len(ids))
	for _, id := range ids {
		definition, err := adaptExtensionDefinition(extensions[id], h)
		if err != nil {
			return fmt.Errorf("prepare extension %q: %w", id, err)
		}
		definitions = append(definitions, definition)
	}
	err := h.manager.Reconcile(ctx, definitions)
	if err == nil || reconcileCommitted(err) {
		h.publishRuntimeOwners()
	}
	return err
}

func (h *Host) publishRuntimeOwners() {
	if h == nil || h.manager == nil {
		return
	}
	snapshot, err := h.manager.AcquireSnapshot()
	if err != nil {
		return
	}
	owners := snapshot.Owners()
	if h.Registry != nil {
		h.Registry.PublishOwners(owners)
	}
	h.resolver.PublishOwners(owners)
	h.hookRegistry.PublishOwners(owners)
	snapshot.Release()
}

func cloneExtensionDefinitions(source map[string]ExtensionDefinition) map[string]ExtensionDefinition {
	copy := make(map[string]ExtensionDefinition, len(source)+1)
	for id, definition := range source {
		copy[id] = definition
	}
	return copy
}

func reconcileCommitted(err error) bool {
	var reconcileErr *component.ReconcileError
	return errors.As(err, &reconcileErr) && reconcileErr.Committed
}

// Status returns the component status for an installed extension ID.
func (h *Host) Status(id string) (component.ComponentStatus, bool) {
	if h == nil || h.manager == nil {
		return component.ComponentStatus{}, false
	}
	return h.manager.Status("extensions/" + id)
}

// Uninstall closes one extension scope and removes only that extension's
// scoped registrations. Other installed extensions remain active.
func (h *Host) Uninstall(ctx context.Context, id string) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h.installMu.Lock()
	defer h.installMu.Unlock()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errors.New("extension host is closed")
	}
	desired := cloneExtensionDefinitions(h.extensions)
	if _, exists := desired[id]; !exists {
		status, known := h.manager.Status("extensions/" + id)
		h.mu.Unlock()
		if known && status.State == component.ComponentClosed {
			return nil
		}
		if known && status.State == component.ComponentStopping {
			h.mu.Lock()
			desired = cloneExtensionDefinitions(h.extensions)
			h.mu.Unlock()
			return h.reconcileExtensionsLocked(ctx, desired)
		}
		return fmt.Errorf("extension %q is not installed", id)
	}
	delete(desired, id)
	h.mu.Unlock()
	err := h.reconcileExtensionsLocked(ctx, desired)
	if err == nil || reconcileCommitted(err) {
		h.mu.Lock()
		h.extensions = desired
		h.mu.Unlock()
	}
	return err
}

func (h *Host) startExtension(ctx context.Context, ext Extension, scope *component.Scope) error {
	if err := scope.Track(ext.Close); err != nil {
		return fmt.Errorf("track extension %q cleanup: %w", ext.ID(), err)
	}
	if scoped, ok := ext.(ScopedInstaller); ok {
		return scoped.InstallScoped(ctx, h, scope)
	}
	return ext.Install(h)
}

// RegisterTool registers a tool owned by scope. The scope tracks the handle
// automatically so failed starts and shutdown both remove it and drain calls.
func (h *Host) RegisterTool(scope *component.Scope, descriptor tools.ToolDescriptor, handler tools.ToolHandler) (*tools.ToolRegistration, error) {
	if h == nil || h.Registry == nil || scope == nil {
		return nil, errors.New("extension host, registry, and component scope are required")
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return nil, errors.New("extension host is closed")
	}
	registration, err := h.Registry.RegisterScoped(descriptor, handler, scope.Owner(), scope.Generation())
	if err != nil {
		return nil, err
	}
	if err := scope.Track(registration.Close); err != nil {
		_ = registration.Close(context.Background())
		return nil, err
	}
	return registration, nil
}

// RegisterSource registers a context source owned by scope. The scope tracks
// the handle automatically and resolver caches are invalidated on removal.
func (h *Host) RegisterSource(scope *component.Scope, source ycontext.ContextSource) (*ycontext.SourceRegistration, error) {
	if h == nil || scope == nil {
		return nil, errors.New("extension host and component scope are required")
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return nil, errors.New("extension host is closed")
	}
	registration, err := h.resolver.RegisterScoped(source, scope.Owner(), scope.Generation())
	if err != nil {
		return nil, err
	}
	if err := scope.Track(registration.Close); err != nil {
		_ = registration.Close(context.Background())
		return nil, err
	}
	return registration, nil
}

// RegisterHooks registers lifecycle hooks owned by scope. Calls already in
// progress drain before the scope completes closing.
func (h *Host) RegisterHooks(scope *component.Scope, hooks agent.RuntimeHooks) (*agent.HookRegistration, error) {
	if h == nil || scope == nil {
		return nil, errors.New("extension host and component scope are required")
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return nil, errors.New("extension host is closed")
	}
	registration, err := h.hookRegistry.RegisterScoped(hooks, scope.Owner(), scope.Generation())
	if err != nil {
		return nil, err
	}
	if err := scope.Track(registration.Close); err != nil {
		_ = registration.Close(context.Background())
		return nil, err
	}
	return registration, nil
}

// AddSource adds a host-lifetime context source. Use RegisterSource from a
// ScopedInstaller when the source must be removed with one extension.
func (h *Host) AddSource(source ycontext.ContextSource) error {
	if h == nil {
		return errors.New("extension host is nil")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errors.New("extension host is closed")
	}
	registration, err := h.resolver.Register(source)
	if err != nil {
		return err
	}
	h.hostCleanups = append(h.hostCleanups, registration.Close)
	return nil
}

// AddHooks adds host-lifetime hooks. Use RegisterHooks from a ScopedInstaller
// when hooks must be removed with one extension.
func (h *Host) AddHooks(hooks agent.RuntimeHooks) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	registration, err := h.hookRegistry.Register(hooks)
	if err == nil {
		h.hostCleanups = append(h.hostCleanups, registration.Close)
	}
}

// AddAgentOption adds a host-lifetime option that is applied to every agent
// composed with this host.
func (h *Host) AddAgentOption(option agent.Option) {
	if h == nil || option == nil {
		return
	}
	h.mu.Lock()
	if !h.closed {
		h.agentOptions = append(h.agentOptions, option)
	}
	h.mu.Unlock()
}

// AgentOptions returns options connected to immutable host runtime snapshots.
// Each agent turn captures tools, context, hooks, services, and their owners
// from one committed component generation.
func (h *Host) AgentOptions() []agent.Option {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	options := append([]agent.Option(nil), h.agentOptions...)
	h.mu.Unlock()
	options = append(options, agent.WithRuntimeViewSource(h))
	return options
}

// Close closes installed scopes and host-lifetime registrations in reverse
// order. A caller deadline limits waiting; shutdown continues and later Close
// calls can observe the same completion result.
func (h *Host) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h.installMu.Lock()
	h.mu.Lock()
	if h.closeDone == nil {
		h.closed = true
		h.closeDone = make(chan struct{})
		manager := h.manager
		cleanups := append([]component.Cleanup(nil), h.hostCleanups...)
		done := h.closeDone
		h.mu.Unlock()
		h.installMu.Unlock()
		cleanupCtx := context.WithoutCancel(ctx)
		go func() {
			closeErr := manager.Close(cleanupCtx)
			for i := len(cleanups) - 1; i >= 0; i-- {
				if err := cleanups[i](cleanupCtx); err != nil {
					closeErr = errors.Join(closeErr, err)
				}
			}
			h.mu.Lock()
			h.closeErr = closeErr
			close(done)
			h.mu.Unlock()
		}()
		return waitHostClose(ctx, done, h)
	}
	done := h.closeDone
	h.mu.Unlock()
	h.installMu.Unlock()
	return waitHostClose(ctx, done, h)
}

func waitHostClose(ctx context.Context, done <-chan struct{}, host *Host) error {
	select {
	case <-done:
		host.mu.Lock()
		defer host.mu.Unlock()
		return host.closeErr
	default:
	}
	select {
	case <-done:
		host.mu.Lock()
		defer host.mu.Unlock()
		return host.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
