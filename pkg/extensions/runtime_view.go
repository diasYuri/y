package extensions

import (
	"context"
	"errors"
	"fmt"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/tools"
)

// AcquireRuntimeView captures the component manager and all owner-scoped
// registries from one committed generation. The returned view pins old scopes
// until the agent finishes the operation that uses it.
func (h *Host) AcquireRuntimeView(ctx context.Context) (*agent.RuntimeView, error) {
	if h == nil {
		return nil, errors.New("extension host is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	closed := h.closed
	manager := h.manager
	registry := h.Registry
	resolver := h.resolver
	hooks := h.hookRegistry
	h.mu.Unlock()
	if closed {
		return nil, errors.New("extension host is closed")
	}

	components, err := manager.AcquireSnapshot()
	if err != nil {
		return nil, err
	}
	owners := components.Owners()
	var toolSnapshot = (*tools.RegistrySnapshot)(nil)
	if registry != nil {
		toolSnapshot, err = registry.Snapshot(owners)
		if err != nil {
			components.Release()
			return nil, err
		}
	}
	contextSnapshot, err := resolver.Snapshot(owners)
	if err != nil {
		if toolSnapshot != nil {
			_ = toolSnapshot.Close()
		}
		components.Release()
		return nil, err
	}
	hookSnapshot, err := hooks.Snapshot(owners)
	if err != nil {
		_ = contextSnapshot.Close()
		if toolSnapshot != nil {
			_ = toolSnapshot.Close()
		}
		components.Release()
		return nil, err
	}

	viewID := fmt.Sprintf("%s:tools-%s:context-%s:hooks-%s", components.ID(),
		toolIdentity(toolSnapshot), contextSnapshot.Identity(), hookSnapshot.Identity())
	info := agent.RuntimeViewInfo{
		ID:         viewID,
		Generation: components.Version(),
		Components: components.Components(),
		Services:   components.Services(),
		ContextSources: func() []agent.ContextSourceOrigin {
			origins := contextSnapshot.Origins()
			out := make([]agent.ContextSourceOrigin, 0, len(origins))
			for _, origin := range origins {
				out = append(out, agent.ContextSourceOrigin{
					Index: origin.Index, Name: origin.Name, Owner: origin.Owner, Generation: origin.Generation,
				})
			}
			return out
		}(),
	}
	var viewRegistry agent.ToolRegistry
	if toolSnapshot != nil {
		viewRegistry = toolSnapshot
		for _, descriptor := range toolSnapshot.List() {
			owner, generation, _ := toolSnapshot.Owner(descriptor.Name)
			info.Tools = append(info.Tools, agent.ToolOrigin{Name: descriptor.Name, Owner: owner, Generation: generation})
		}
	}
	viewHooks := hookSnapshot.Hooks()
	for index := range viewHooks {
		owner, generation, _ := hookSnapshot.Owner(index)
		info.Hooks = append(info.Hooks, agent.HookOrigin{Index: index, Owner: owner, Generation: generation})
	}
	view, err := agent.NewRuntimeView(info, viewRegistry, contextSnapshot, viewHooks, func() error {
		var result error
		result = errors.Join(result, hookSnapshot.Close())
		result = errors.Join(result, contextSnapshot.Close())
		if toolSnapshot != nil {
			result = errors.Join(result, toolSnapshot.Close())
		}
		components.Release()
		return result
	})
	if err != nil {
		_ = hookSnapshot.Close()
		_ = contextSnapshot.Close()
		if toolSnapshot != nil {
			_ = toolSnapshot.Close()
		}
		components.Release()
		return nil, err
	}
	return view, nil
}

func toolIdentity(snapshot *tools.RegistrySnapshot) string {
	if snapshot == nil {
		return "empty"
	}
	return snapshot.Identity()
}
