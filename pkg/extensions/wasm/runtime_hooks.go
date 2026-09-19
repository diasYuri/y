package wasm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/diasYuri/y/pkg/agent"
)

// RuntimeHooks adapts one or more WASM extensions into Agent lifecycle hooks.
// It is opt-in: callers retain complete control over which extensions observe
// a run and any extension error rejects the active safe-point operation.
func RuntimeHooks(manager Manager, extensionIDs ...string) agent.RuntimeHooks {
	ids := append([]string(nil), extensionIDs...)
	dispatch := func(ctx context.Context, phase string, payload any) error {
		if manager == nil {
			return fmt.Errorf("WASM runtime hook manager is nil")
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal WASM runtime hook %s: %w", phase, err)
		}
		body, err := json.Marshal(LifecycleRequest{Phase: phase, Payload: raw})
		if err != nil {
			return fmt.Errorf("marshal WASM lifecycle request: %w", err)
		}
		for _, id := range ids {
			if _, err := manager.CallRuntime(ctx, id, RuntimeRequest{Kind: KindLifecycle, Payload: body}); err != nil {
				return fmt.Errorf("WASM runtime hook %s for %s: %w", phase, id, err)
			}
		}
		return nil
	}
	return agent.RuntimeHooks{
		BeforeTurn: func(ctx context.Context, turn agent.TurnContext) error { return dispatch(ctx, "before_turn", turn) },
		AfterTurn: func(ctx context.Context, turn agent.TurnContext, result agent.RunResult) error {
			return dispatch(ctx, "after_turn", struct {
				Turn   agent.TurnContext `json:"turn"`
				Result agent.RunResult   `json:"result"`
			}{turn, result})
		},
		BeforeCompaction: func(ctx context.Context) error { return dispatch(ctx, "before_compaction", nil) },
		OnSession: func(ctx context.Context, event agent.SessionEvent) error {
			return dispatch(ctx, "session_lifecycle", event)
		},
	}
}
