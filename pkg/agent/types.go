package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/policy"
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
	StateWaitingApproval State = "waiting_approval"
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
	EventStateChanged        EventKind = "state_changed"
	EventAgentStarted        EventKind = "agent_started"
	EventAgentCompleted      EventKind = "agent_completed"
	EventAgentSettled        EventKind = "agent_settled"
	EventMessageStarted      EventKind = "message_started"
	EventMessageDelta        EventKind = "message_delta"
	EventMessageCompleted    EventKind = "message_completed"
	EventThinkingDelta       EventKind = "thinking_delta"
	EventTurnStarted         EventKind = "turn_started"
	EventTurnCompleted       EventKind = "turn_completed"
	EventTurnEnded           EventKind = "turn_ended" // compatibility alias value
	EventToolStarted         EventKind = "tool_started"
	EventToolProgress        EventKind = "tool_progress"
	EventToolCompleted       EventKind = "tool_completed"
	EventToolEnded           EventKind = "tool_ended" // compatibility alias value
	EventRetryScheduled      EventKind = "retry_scheduled"
	EventCompactionStarted   EventKind = "compaction_started"
	EventCompactionCompleted EventKind = "compaction_completed"
	EventStateCheckpointed   EventKind = "state_checkpointed"
	EventAbortRequested      EventKind = "abort_requested"
	EventAbortCompleted      EventKind = "abort_completed"
	EventTextDelta           EventKind = "text_delta" // compatibility alias value
	EventCompleted           EventKind = "completed"  // compatibility alias value
)

// Event is emitted by the agent as it progresses through the loop.
type Event struct {
	Kind           EventKind          `json:"kind"`
	State          State              `json:"state,omitempty"`
	RunID          string             `json:"run_id,omitempty"`
	SessionID      string             `json:"session_id,omitempty"`
	TurnID         string             `json:"turn_id,omitempty"`
	ToolCallID     string             `json:"tool_call_id,omitempty"`
	Sequence       uint64             `json:"sequence,omitempty"`
	Timestamp      time.Time          `json:"timestamp,omitempty"`
	CorrelationID  string             `json:"correlation_id,omitempty"`
	IdempotencyKey string             `json:"idempotency_key,omitempty"`
	Turn           int                `json:"turn,omitempty"`
	Message        ai.Message         `json:"message,omitempty"`
	ToolCall       ai.ToolCall        `json:"tool_call,omitempty"`
	ToolResult     ai.ToolResult      `json:"tool_result,omitempty"`
	ToolProgress   tools.ToolProgress `json:"tool_progress,omitempty"`
	Usage          ai.Usage           `json:"usage,omitempty"`
	TextDelta      string             `json:"text_delta,omitempty"`
	ThinkingDelta  string             `json:"thinking_delta,omitempty"`
	Payload        json.RawMessage    `json:"payload,omitempty"`
	Err            error              `json:"-"`
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
	Messages   []ai.Message            `json:"messages,omitempty"`
	Usage      ai.Usage                `json:"usage,omitempty"`
	Turns      int                     `json:"turns,omitempty"`
	State      State                   `json:"state,omitempty"`
	StopReason ai.StopReason           `json:"stop_reason,omitempty"`
	Model      ai.Model                `json:"model,omitempty"`
	Error      *ai.ProviderError       `json:"error,omitempty"`
	Approval   *policy.ApprovalRequest `json:"approval,omitempty"`
}

// PendingApproval is the durable continuation point for a tool invocation
// that cannot proceed until a remote caller supplies a signed resolution.
type PendingApproval struct {
	Request  policy.ApprovalRequest `json:"request"`
	ToolCall ai.ToolCall            `json:"tool_call"`
	Turn     int                    `json:"turn"`
}

// Option configures an Agent.
type Option func(*Agent)
