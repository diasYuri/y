package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ycontext "github.com/diasYuri/y/pkg/context"
	"github.com/diasYuri/y/pkg/tools"
)

var runtimeViewProcessID = newID()

func (a *Agent) acquireRuntimeView(ctx context.Context) (*RuntimeView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	a.mu.Lock()
	source := a.runtimeViewSource
	registry := a.registry
	resolver := a.contextResolver
	instanceID := a.runtimeViewInstanceID
	hooks := append([]RuntimeHooks(nil), a.runtimeHooks...)
	hookRegistries := append([]*RuntimeHookRegistry(nil), a.runtimeHookRegistries...)
	a.mu.Unlock()

	if source != nil {
		view, err := source.AcquireRuntimeView(ctx)
		if err != nil {
			return nil, err
		}
		if view == nil {
			return nil, errors.New("agent: runtime view source returned a nil view")
		}
		view.approvalBound = true
		view.info.ID = fmt.Sprintf("%s:agent-%s", view.info.ID, instanceID)
		if view.Tools() == nil && registry != nil {
			viewRegistry := registry
			if concrete, ok := registry.(*tools.Registry); ok {
				if concrete == nil {
					viewRegistry = nil
				} else {
					snapshot, snapErr := concrete.Snapshot(nil)
					if snapErr != nil {
						_ = view.Close()
						return nil, snapErr
					}
					viewRegistry = snapshot
					view.info.ID = fmt.Sprintf("%s:agent-tools-%s", view.info.ID, snapshot.Identity())
					for _, descriptor := range snapshot.List() {
						owner, generation, _ := snapshot.Owner(descriptor.Name)
						view.info.Tools = append(view.info.Tools, ToolOrigin{Name: descriptor.Name, Owner: owner, Generation: generation})
					}
					oldRelease := view.release
					view.release = func() error {
						return errors.Join(snapshot.Close(), callRelease(oldRelease))
					}
				}
			} else {
				view.info.ID += ":agent-tools-custom"
				for _, descriptor := range registry.List() {
					view.info.Tools = append(view.info.Tools, ToolOrigin{Name: descriptor.Name})
				}
			}
			view.registry = viewRegistry
		}
		if view.ContextResolver() == nil && resolver != nil {
			snapshot, snapErr := resolver.Snapshot(nil)
			if snapErr != nil {
				_ = view.Close()
				return nil, snapErr
			}
			view.resolver = snapshot
			view.info.ID = fmt.Sprintf("%s:agent-context-%s", view.info.ID, snapshot.Identity())
			for _, origin := range snapshot.Origins() {
				view.info.ContextSources = append(view.info.ContextSources, ContextSourceOrigin{
					Index: origin.Index, Name: origin.Name, Owner: origin.Owner, Generation: origin.Generation,
				})
			}
			oldRelease := view.release
			view.release = func() error {
				return errors.Join(snapshot.Close(), callRelease(oldRelease))
			}
		}

		view.hooks = append(hooks, view.hooks...)
		for _, hookRegistry := range hookRegistries {
			snapshot, snapErr := hookRegistry.Snapshot(nil)
			if snapErr != nil {
				_ = view.Close()
				return nil, snapErr
			}
			view.hooks = append(view.hooks, snapshot.Hooks()...)
			view.info.ID = fmt.Sprintf("%s:agent-hooks-%s", view.info.ID, snapshot.Identity())
			hookOffset := len(view.info.Hooks)
			for index := range snapshot.Hooks() {
				owner, generation, _ := snapshot.Owner(index)
				view.info.Hooks = append(view.info.Hooks, HookOrigin{Index: hookOffset + index, Owner: owner, Generation: generation})
			}
			oldRelease := view.release
			view.release = func() error {
				return errors.Join(snapshot.Close(), callRelease(oldRelease))
			}
		}
		a.wrapRuntimeTools(view, registry)
		return view, nil
	}

	var toolSnapshot *tools.RegistrySnapshot
	viewRegistry := registry
	if concrete, ok := registry.(*tools.Registry); ok {
		if concrete == nil {
			viewRegistry = nil
		} else {
			var err error
			toolSnapshot, err = concrete.Snapshot(nil)
			if err != nil {
				return nil, err
			}
			viewRegistry = toolSnapshot
		}
	}

	var contextSnapshot *ycontext.ResolverSnapshot
	var viewResolver RuntimeContextResolver
	if resolver != nil {
		var err error
		contextSnapshot, err = resolver.Snapshot(nil)
		if err != nil {
			if toolSnapshot != nil {
				_ = toolSnapshot.Close()
			}
			return nil, err
		}
		viewResolver = contextSnapshot
	}

	var hookSnapshots []*RuntimeHooksSnapshot
	for _, hookRegistry := range hookRegistries {
		snapshot, err := hookRegistry.Snapshot(nil)
		if err != nil {
			for _, acquired := range hookSnapshots {
				_ = acquired.Close()
			}
			if contextSnapshot != nil {
				_ = contextSnapshot.Close()
			}
			if toolSnapshot != nil {
				_ = toolSnapshot.Close()
			}
			return nil, err
		}
		hookSnapshots = append(hookSnapshots, snapshot)
		hooks = append(hooks, snapshot.Hooks()...)
	}

	parts := make([]string, 0, 2+len(hookSnapshots))
	if toolSnapshot != nil {
		parts = append(parts, "tools-"+toolSnapshot.Identity())
	}
	if contextSnapshot != nil {
		parts = append(parts, "context-"+contextSnapshot.Identity())
	}
	for _, snapshot := range hookSnapshots {
		parts = append(parts, "hooks-"+snapshot.Identity())
	}
	id := fmt.Sprintf("agent-local:%s:%s", runtimeViewProcessID, instanceID)
	if len(parts) > 0 {
		id += ":" + strings.Join(parts, ":")
	}
	info := RuntimeViewInfo{ID: id}
	if toolSnapshot != nil {
		for _, descriptor := range toolSnapshot.List() {
			owner, generation, _ := toolSnapshot.Owner(descriptor.Name)
			info.Tools = append(info.Tools, ToolOrigin{Name: descriptor.Name, Owner: owner, Generation: generation})
		}
	}
	if contextSnapshot != nil {
		for _, origin := range contextSnapshot.Origins() {
			info.ContextSources = append(info.ContextSources, ContextSourceOrigin{
				Index: origin.Index, Name: origin.Name, Owner: origin.Owner, Generation: origin.Generation,
			})
		}
	}
	for _, snapshot := range hookSnapshots {
		hookOffset := len(info.Hooks)
		for index := range snapshot.Hooks() {
			owner, generation, _ := snapshot.Owner(index)
			info.Hooks = append(info.Hooks, HookOrigin{Index: hookOffset + index, Owner: owner, Generation: generation})
		}
	}
	view, err := NewRuntimeView(info, viewRegistry, viewResolver, hooks, func() error {
		var result error
		for _, snapshot := range hookSnapshots {
			result = errors.Join(result, snapshot.Close())
		}
		if contextSnapshot != nil {
			result = errors.Join(result, contextSnapshot.Close())
		}
		if toolSnapshot != nil {
			result = errors.Join(result, toolSnapshot.Close())
		}
		return result
	})
	if err != nil {
		for _, snapshot := range hookSnapshots {
			_ = snapshot.Close()
		}
		if contextSnapshot != nil {
			_ = contextSnapshot.Close()
		}
		if toolSnapshot != nil {
			_ = toolSnapshot.Close()
		}
	}
	if err == nil {
		a.wrapRuntimeTools(view, registry)
	}
	return view, err
}

func callRelease(release func() error) error {
	if release == nil {
		return nil
	}
	return release()
}

func (a *Agent) wrapRuntimeTools(view *RuntimeView, registry ToolRegistry) {
	if decorator, ok := registry.(interface {
		wrapRuntimeTools(ToolRegistry) ToolRegistry
	}); ok {
		view.registry = decorator.wrapRuntimeTools(view.registry)
	}
}
