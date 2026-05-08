//go:build !feature_wasm_ext

package wasm

import (
	"context"
	"fmt"
)

// disabledManager satisfies Manager without pulling in the wazero runtime.
// Discovery and listings still work so callers can surface useful diagnostics
// in builds that happen to ship without the host.
type disabledManager struct {
	state *state
}

// NewManager returns a Manager that refuses to instantiate modules. Discovery
// and metadata access still operate so that the caller can render extension
// listings and emit accurate diagnostics.
func NewManager(cfg Config) Manager {
	return &disabledManager{state: newState(cfg)}
}

// HostAvailable reports whether this build links the WASM runtime.
func HostAvailable() bool { return false }

func (m *disabledManager) Discover(ctx context.Context) error {
	return m.state.discover(ctx)
}

func (m *disabledManager) List() []ExtensionInfo { return m.state.list() }

func (m *disabledManager) Get(id string) (ExtensionInfo, error) {
	return m.state.get(id)
}

func (m *disabledManager) Load(ctx context.Context, id string) error {
	if _, ok := m.state.lookup(id); !ok {
		return fmt.Errorf("%w: %q", ErrExtensionNotFound, id)
	}
	return ErrHostUnavailable
}

func (m *disabledManager) Unload(ctx context.Context, id string) error {
	if _, ok := m.state.lookup(id); !ok {
		return fmt.Errorf("%w: %q", ErrExtensionNotFound, id)
	}
	return nil
}

func (m *disabledManager) CallTool(ctx context.Context, id string, _ ToolRequest) (ToolResponse, error) {
	if _, ok := m.state.lookup(id); !ok {
		return ToolResponse{}, fmt.Errorf("%w: %q", ErrExtensionNotFound, id)
	}
	return ToolResponse{}, ErrHostUnavailable
}

func (m *disabledManager) Close(ctx context.Context) error { return nil }
