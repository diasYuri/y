package ai

import (
	"encoding/json"
	"errors"
	"time"
)

// API identifies a provider wire protocol such as openai-responses or
// anthropic-messages.
type API string

// ProviderID identifies a provider implementation such as openai or anthropic.
type ProviderID string

// Transport identifies the preferred streaming transport for a request.
type Transport string

const (
	TransportAuto      Transport = "auto"
	TransportSSE       Transport = "sse"
	TransportWebSocket Transport = "websocket"
)

// CacheRetention describes how long a provider should retain prompt cache
// entries when it supports cache controls.
type CacheRetention string

const (
	CacheRetentionNone  CacheRetention = "none"
	CacheRetentionShort CacheRetention = "short"
	CacheRetentionLong  CacheRetention = "long"
)

// ThinkingLevel is the normalized reasoning budget selector.
type ThinkingLevel string

const (
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
)

// Model describes a single provider model.
type Model struct {
	ID            string
	Name          string
	API           API
	Provider      ProviderID
	BaseURL       string
	Reasoning     bool
	Input         []InputKind
	Cost          Cost
	ContextWindow int64
	MaxTokens     int64
	Headers       map[string]string
}

// InputKind identifies model input modalities.
type InputKind string

const (
	InputText  InputKind = "text"
	InputImage InputKind = "image"
)

// Cost stores per-million-token costs in USD.
type Cost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// Role identifies the source of a transcript message.
type Role string

const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "tool_result"
)

// Message is the normalized transcript unit consumed by providers.
type Message struct {
	Role       Role
	Content    []ContentBlock
	ToolCalls  []ToolCall
	ToolResult *ToolResult
	Timestamp  time.Time
}

// ContentBlock is a provider-neutral message content block.
type ContentBlock struct {
	Type             ContentType
	Text             string
	Thinking         string
	ThinkingRedacted bool
	Signature        string
	ImageData        []byte
	ImageMIMEType    string
	ProviderMetadata json.RawMessage
}

// ContentType identifies the populated fields in a ContentBlock.
type ContentType string

const (
	ContentText     ContentType = "text"
	ContentThinking ContentType = "thinking"
	ContentImage    ContentType = "image"
)

// Tool declares a callable tool exposed to a provider.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// ToolCall is a normalized provider request to invoke a tool.
type ToolCall struct {
	ID               string
	Name             string
	Arguments        json.RawMessage
	ThoughtSignature string
}

// ToolResult is a normalized result returned for a prior tool call.
type ToolResult struct {
	ToolCallID string
	ToolName   string
	Content    []ContentBlock
	IsError    bool
	Details    json.RawMessage
}

// Context is the provider input assembled by the agent loop.
type Context struct {
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
}

// Usage is the normalized accounting emitted by providers.
type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	TotalTokens      int64
	Cost             UsageCost
}

// UsageCost stores request cost components in USD.
type UsageCost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
	Total      float64
}

// StopReason is the normalized reason a stream ended.
type StopReason string

const (
	StopReasonStop    StopReason = "stop"
	StopReasonLength  StopReason = "length"
	StopReasonToolUse StopReason = "tool_use"
	StopReasonError   StopReason = "error"
	StopReasonAborted StopReason = "aborted"
)

// EventKind identifies a normalized provider stream event.
type EventKind string

const (
	EventTextDelta EventKind = "text_delta"
	EventToolCall  EventKind = "tool_call"
	EventUsage     EventKind = "usage"
	EventStop      EventKind = "stop"
	EventError     EventKind = "error"
)

// Event is implemented by every normalized provider stream event.
type Event interface {
	Kind() EventKind
}

// TextDelta is a streamed assistant text fragment.
type TextDelta struct {
	ContentIndex int
	Text         string
}

func (TextDelta) Kind() EventKind { return EventTextDelta }

// ToolCallEvent is a streamed tool call. ArgumentsDelta may contain partial
// JSON bytes while ToolCall.Arguments contains the complete arguments when
// Complete is true.
type ToolCallEvent struct {
	ContentIndex   int
	ToolCall       ToolCall
	ArgumentsDelta json.RawMessage
	Complete       bool
}

func (ToolCallEvent) Kind() EventKind { return EventToolCall }

// UsageEvent reports normalized token and cost usage.
type UsageEvent struct {
	Usage Usage
}

func (UsageEvent) Kind() EventKind { return EventUsage }

// StopEvent reports normal or provider-requested stream termination.
type StopEvent struct {
	Reason StopReason
}

func (StopEvent) Kind() EventKind { return EventStop }

// ErrorEvent reports a provider-normalized stream error without forcing the
// stream transport itself to fail.
type ErrorEvent struct {
	Code      string
	Message   string
	Retryable bool
	Err       error
}

func (ErrorEvent) Kind() EventKind { return EventError }

func (e ErrorEvent) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Code != "" {
		return e.Code
	}
	return "provider stream error"
}

func (e ErrorEvent) Unwrap() error {
	return e.Err
}

// NewErrorEvent converts err into a stream error event. A nil err produces a
// generic non-retryable error event.
func NewErrorEvent(code string, err error) ErrorEvent {
	if err == nil {
		err = errors.New("provider stream error")
	}
	return ErrorEvent{
		Code:    code,
		Message: err.Error(),
		Err:     err,
	}
}
