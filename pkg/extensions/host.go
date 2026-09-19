package extensions

import (
	"context"
	"errors"
	"sync"

	"github.com/diasYuri/y/pkg/agent"
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

// Host composes optional extensions for one runtime instance.
type Host struct {
	Registry     *tools.Registry
	sources      []ycontext.ContextSource
	hooks        []agent.RuntimeHooks
	agentOptions []agent.Option
	installed    []Extension
	closeOnce    sync.Once
	closeErr     error
}

// NewHost creates an extension host backed by registry.
func NewHost(registry *tools.Registry) *Host {
	return &Host{Registry: registry}
}

// Install installs and tracks ext. Failed installations are not retained.
func (h *Host) Install(ext Extension) error {
	if h == nil {
		return errors.New("extension host is nil")
	}
	if ext == nil {
		return errors.New("extension is nil")
	}
	if err := ext.Install(h); err != nil {
		return err
	}
	h.installed = append(h.installed, ext)
	return nil
}

// AddSource adds a context source to the composed resolver.
func (h *Host) AddSource(source ycontext.ContextSource) error {
	if h == nil {
		return errors.New("extension host is nil")
	}
	if source == nil {
		return errors.New("context source is nil")
	}
	h.sources = append(h.sources, source)
	return nil
}

// AddHooks adds lifecycle hooks to the composed runtime.
func (h *Host) AddHooks(hooks agent.RuntimeHooks) {
	if h != nil {
		h.hooks = append(h.hooks, hooks)
	}
}

// AddAgentOption adds an option that must be applied to every agent composed
// with this host. This lets the application provide runtime identity without
// making the core agent know which extension needs it.
func (h *Host) AddAgentOption(option agent.Option) {
	if h != nil && option != nil {
		h.agentOptions = append(h.agentOptions, option)
	}
}

// AgentOptions returns options that connect the installed extensions to an
// agent. Sources share one resolver so their precedence and cache are owned by
// the host rather than by any particular extension.
func (h *Host) AgentOptions() []agent.Option {
	if h == nil {
		return nil
	}
	options := make([]agent.Option, 0, len(h.agentOptions)+len(h.hooks)+1)
	options = append(options, h.agentOptions...)
	if len(h.sources) > 0 {
		options = append(options, agent.WithContextResolver(ycontext.NewResolver(h.sources...)))
	}
	for _, hooks := range h.hooks {
		options = append(options, agent.WithRuntimeHooks(hooks))
	}
	return options
}

// Close closes installed extensions in reverse installation order.
func (h *Host) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		for i := len(h.installed) - 1; i >= 0; i-- {
			if err := h.installed[i].Close(ctx); err != nil {
				h.closeErr = errors.Join(h.closeErr, err)
			}
		}
	})
	return h.closeErr
}
