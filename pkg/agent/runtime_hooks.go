package agent

import (
	"context"

	"github.com/yuri/y/pkg/agent/compaction"
	"github.com/yuri/y/pkg/ai"
)

// TurnContext identifies one provider/tool turn in a run.
type TurnContext struct {
	RunID     string
	SessionID string
	Turn      int
}

// SessionEvent identifies lifecycle transitions that do not belong to a
// concrete provider request (run started, settled, or restored).
type SessionEvent struct {
	RunID     string
	SessionID string
	State     State
}

// ResourceProvider supplies request-scoped messages from a database, object
// store, or other context system. Returned messages are used only for the
// current provider request and are never implicitly persisted in transcript.
type ResourceProvider func(context.Context) ([]ai.Message, error)

// RequestToolProvider adds provider-visible tools for a request. It is useful
// for remote tool backends that are represented by the configured registry.
type RequestToolProvider func(context.Context) ([]ai.Tool, error)

// RuntimeHooks groups optional lifecycle extensions. Hooks are process-local
// collaborators and are intentionally excluded from snapshots; a worker must
// register them again after a handoff. Returning an error from a before hook
// rejects that operation. AfterTool can mutate the result to implement a
// policy-approved result override.
type RuntimeHooks struct {
	BeforeRequest    BeforeRequestHook
	AfterRequest     AfterRequestHook
	OnError          ErrorHook
	BeforeMessage    func(context.Context, *ai.Message) error
	BeforeTurn       func(context.Context, TurnContext) error
	AfterTurn        func(context.Context, TurnContext, RunResult) error
	BeforeTool       func(context.Context, *ai.ToolCall) error
	AfterTool        func(context.Context, ai.ToolCall, *ai.ToolResult) error
	BeforeModel      func(context.Context, *ai.Model) error
	AfterModel       func(context.Context, ai.Model, error) error
	BeforeCompaction func(context.Context) error
	AfterCompaction  func(context.Context, compaction.Result, error)
	OnSession        func(context.Context, SessionEvent) error
	Resources        ResourceProvider
	Tools            RequestToolProvider
}

func (a *Agent) hooksSnapshot() []RuntimeHooks {
	a.mu.Lock()
	hooks := append([]RuntimeHooks(nil), a.runtimeHooks...)
	a.mu.Unlock()
	return hooks
}

func (a *Agent) notifySession(ctx context.Context, state State) error {
	a.mu.Lock()
	event := SessionEvent{RunID: a.runID, SessionID: a.sessionID, State: state}
	a.mu.Unlock()
	for _, hooks := range a.hooksSnapshot() {
		if hooks.OnSession != nil {
			if err := hooks.OnSession(ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *Agent) applyBeforeTurn(ctx context.Context, turn int) error {
	a.mu.Lock()
	value := TurnContext{RunID: a.runID, SessionID: a.sessionID, Turn: turn}
	a.mu.Unlock()
	for _, hooks := range a.hooksSnapshot() {
		if hooks.BeforeTurn != nil {
			if err := hooks.BeforeTurn(ctx, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *Agent) applyAfterTurn(ctx context.Context, turn int, result RunResult) error {
	a.mu.Lock()
	value := TurnContext{RunID: a.runID, SessionID: a.sessionID, Turn: turn}
	a.mu.Unlock()
	for _, hooks := range a.hooksSnapshot() {
		if hooks.AfterTurn != nil {
			if err := hooks.AfterTurn(ctx, value, result); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *Agent) applyAfterModel(ctx context.Context, model ai.Model, hookErr error) error {
	for _, hooks := range a.hooksSnapshot() {
		if hooks.AfterModel != nil {
			if err := hooks.AfterModel(ctx, model, hookErr); err != nil {
				return err
			}
		}
	}
	return hookErr
}
