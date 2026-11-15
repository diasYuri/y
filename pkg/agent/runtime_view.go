package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/diasYuri/y/pkg/component"
	ycontext "github.com/diasYuri/y/pkg/context"
)

// RuntimeContextResolver is the request context boundary captured by a view.
type RuntimeContextResolver interface {
	Resolve(context.Context, ycontext.Request) (ycontext.Result, error)
}

// RuntimeViewSource provides immutable operation views. Implementations pin
// registrations and component scopes until the returned view is closed.
type RuntimeViewSource interface {
	AcquireRuntimeView(context.Context) (*RuntimeView, error)
}

// ToolOrigin identifies the component generation that registered a tool.
type ToolOrigin struct {
	Name       string `json:"name"`
	Owner      string `json:"owner,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
}

// RuntimeViewInfo contains portable diagnostics for one immutable operation
// view. It excludes handlers, services and other live runtime values.
type RuntimeViewInfo struct {
	ID             string                          `json:"id"`
	Generation     uint64                          `json:"generation,omitempty"`
	Components     []component.ComponentGeneration `json:"components,omitempty"`
	Services       []component.ServiceBindingInfo  `json:"services,omitempty"`
	Tools          []ToolOrigin                    `json:"tools,omitempty"`
	ContextSources []ContextSourceOrigin           `json:"context_sources,omitempty"`
	Hooks          []HookOrigin                    `json:"hooks,omitempty"`
}

// ContextSourceOrigin identifies one captured context source.
type ContextSourceOrigin struct {
	Index      int    `json:"index"`
	Name       string `json:"name"`
	Version    string `json:"version,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
}

// HookOrigin identifies a captured hook registration.
type HookOrigin struct {
	Index      int    `json:"index"`
	Owner      string `json:"owner,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
}

// RuntimeView binds the tools, context sources, hooks and component metadata
// that one agent turn must use together.
type RuntimeView struct {
	infoMu        sync.RWMutex
	info          RuntimeViewInfo
	registry      ToolRegistry
	resolver      RuntimeContextResolver
	hooks         []RuntimeHooks
	approvalBound bool
	release       func() error
	close         sync.Once
	closeErr      error
}

// NewRuntimeView creates an immutable runtime view. The caller retains
// ownership of release and should close the view once the operation completes.
func NewRuntimeView(info RuntimeViewInfo, registry ToolRegistry, resolver RuntimeContextResolver, hooks []RuntimeHooks, release func() error) (*RuntimeView, error) {
	if info.ID == "" {
		return nil, errors.New("agent: runtime view ID is required")
	}
	info = cloneRuntimeViewInfo(info)
	return &RuntimeView{info: info, registry: registry, resolver: resolver, hooks: append([]RuntimeHooks(nil), hooks...), release: release}, nil
}

// Info returns a defensive copy of view diagnostics.
func (v *RuntimeView) Info() RuntimeViewInfo {
	if v == nil {
		return RuntimeViewInfo{}
	}
	v.infoMu.RLock()
	defer v.infoMu.RUnlock()
	return cloneRuntimeViewInfo(v.info)
}

func cloneRuntimeViewInfo(info RuntimeViewInfo) RuntimeViewInfo {
	info.Components = append([]component.ComponentGeneration(nil), info.Components...)
	info.Services = append([]component.ServiceBindingInfo(nil), info.Services...)
	info.Tools = append([]ToolOrigin(nil), info.Tools...)
	info.ContextSources = append([]ContextSourceOrigin(nil), info.ContextSources...)
	info.Hooks = append([]HookOrigin(nil), info.Hooks...)
	return info
}

func cloneRuntimeViewInfos(infos []RuntimeViewInfo) []RuntimeViewInfo {
	if infos == nil {
		return nil
	}
	out := make([]RuntimeViewInfo, len(infos))
	for i, info := range infos {
		out[i] = cloneRuntimeViewInfo(info)
	}
	return out
}

// RecordContextSources attaches the data versions used by a request to this
// view's diagnostics. The captured source registrations remain unchanged.
func (v *RuntimeView) RecordContextSources(sources []ycontext.SourceInfo) {
	if v == nil || len(sources) == 0 {
		return
	}
	v.infoMu.Lock()
	byName := make(map[string][]string, len(sources))
	for _, source := range sources {
		byName[source.Name] = append(byName[source.Name], source.Version)
	}
	seen := make(map[string]int, len(byName))
	for i := range v.info.ContextSources {
		name := v.info.ContextSources[i].Name
		versions := byName[name]
		index := seen[name]
		if index < len(versions) {
			v.info.ContextSources[i].Version = versions[index]
			seen[name] = index + 1
		}
	}
	v.infoMu.Unlock()
}

// ID returns the opaque identity used to bind deferred approvals to the view
// that originally advertised their tool call.
func (v *RuntimeView) ID() string {
	if v == nil {
		return ""
	}
	return v.info.ID
}

// Tools returns the captured tool registry view.
func (v *RuntimeView) Tools() ToolRegistry {
	if v == nil {
		return nil
	}
	return v.registry
}

// ContextResolver returns the captured context resolver.
func (v *RuntimeView) ContextResolver() RuntimeContextResolver {
	if v == nil {
		return nil
	}
	return v.resolver
}

func (v *RuntimeView) bindsPendingApprovals() bool {
	return v != nil && v.approvalBound
}

// Hooks returns a copy of captured lifecycle hooks.
func (v *RuntimeView) Hooks() []RuntimeHooks {
	if v == nil {
		return nil
	}
	return append([]RuntimeHooks(nil), v.hooks...)
}

// Close releases all resources held by this view and is safe to call repeatedly.
func (v *RuntimeView) Close() error {
	if v == nil {
		return nil
	}
	v.close.Do(func() {
		if v.release != nil {
			v.closeErr = v.release()
		}
	})
	return v.closeErr
}

type runtimeViewContextKey struct{}

func withRuntimeView(ctx context.Context, view *RuntimeView) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, runtimeViewContextKey{}, view)
}

// RuntimeViewFromContext returns the view captured for the current operation.
func RuntimeViewFromContext(ctx context.Context) (*RuntimeView, bool) {
	if ctx == nil {
		return nil, false
	}
	view, ok := ctx.Value(runtimeViewContextKey{}).(*RuntimeView)
	return view, ok && view != nil
}
