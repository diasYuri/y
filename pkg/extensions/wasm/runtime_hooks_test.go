package wasm

import (
	"context"
	"testing"

	"github.com/yuri/y/pkg/agent"
)

type runtimeHookManager struct{ calls int }

func (m *runtimeHookManager) Discover(context.Context) error       { return nil }
func (m *runtimeHookManager) List() []ExtensionInfo                { return nil }
func (m *runtimeHookManager) Get(string) (ExtensionInfo, error)    { return ExtensionInfo{}, nil }
func (m *runtimeHookManager) Load(context.Context, string) error   { return nil }
func (m *runtimeHookManager) Unload(context.Context, string) error { return nil }
func (m *runtimeHookManager) CallTool(context.Context, string, ToolRequest) (ToolResponse, error) {
	return ToolResponse{}, nil
}
func (m *runtimeHookManager) CallRuntime(_ context.Context, _ string, request RuntimeRequest) (RuntimeResponse, error) {
	m.calls++
	if request.Kind != KindLifecycle {
		return RuntimeResponse{}, ErrHostUnavailable
	}
	return RuntimeResponse{}, nil
}
func (m *runtimeHookManager) Close(context.Context) error { return nil }

func TestRuntimeHooksDispatchLifecycle(t *testing.T) {
	manager := &runtimeHookManager{}
	hooks := RuntimeHooks(manager, "extension")
	if err := hooks.BeforeTurn(context.Background(), agent.TurnContext{RunID: "run", Turn: 1}); err != nil {
		t.Fatal(err)
	}
	if manager.calls != 1 {
		t.Fatalf("calls = %d", manager.calls)
	}
}
