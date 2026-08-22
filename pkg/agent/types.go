package agent

import (
	"context"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/tools"
)

// State describes the current phase of an agent run.
type State string

const (
	StateIdle            State = "idle"
	StateSelectingModel  State = "selecting_model"
	StateRequestingModel State = "requesting_model"
	StateStreaming       State = "streaming"
	StateExecutingTools  State = "executing_tools"
	StateCompleted       State = "completed"
	StateCanceled        State = "canceled"
	StateFailed          State = "failed"
)

// ToolExecutionMode controls whether a tool batch runs sequentially or in
// parallel.
//
// In parallel mode the agent waits for every tool call in the batch to
// finish before propagating the first error it sees. Peers are NOT cancelled
// when one tool fails — they continue to run and have their results
// recorded. Callers that need fail-fast cancellation across the batch
// should run their tools in sequential mode (or wrap their handlers to
// cooperate via the shared run context).
type ToolExecutionMode string

const (
	ToolExecutionSequential ToolExecutionMode = "sequential"
	ToolExecutionParallel   ToolExecutionMode = "parallel"
)

// EventKind identifies a state-machine notification.
type EventKind string

const (
	EventStateChanged EventKind = "state_changed"
	EventTurnStarted  EventKind = "turn_started"
	EventTurnEnded    EventKind = "turn_ended"
	EventTextDelta    EventKind = "text_delta"
	EventToolStarted  EventKind = "tool_started"
	EventToolEnded    EventKind = "tool_ended"
	EventCompleted    EventKind = "completed"
)

// Event is emitted by the agent as it progresses through the loop.
type Event struct {
	Kind       EventKind
	State      State
	Turn       int
	Message    ai.Message
	ToolCall   ai.ToolCall
	ToolResult ai.ToolResult
	Usage      ai.Usage
	TextDelta  string
	Err        error
}

// EventSink receives state-machine events.
type EventSink func(Event)

// Provider describes the subset of providers.Provider used by the agent loop.
type Provider interface {
	ID() string
	Models(context.Context) ([]ai.Model, error)
	Stream(context.Context, providers.StreamRequest) (providers.EventStream, error)
}

// ToolRegistry describes the subset of pkg/tools.Registry used by the agent loop.
type ToolRegistry interface {
	List() []tools.ToolDescriptor
	Handle(context.Context, tools.ToolRequest) (tools.ToolResponse, error)
	GetExecutionMode(name string) tools.ExecutionMode
}

// RunResult summarizes a completed or failed run.
type RunResult struct {
	Messages   []ai.Message
	Usage      ai.Usage
	Turns      int
	State      State
	StopReason ai.StopReason
	Model      ai.Model
}

// Option configures an Agent.
type Option func(*Agent)
