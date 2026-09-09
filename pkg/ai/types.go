package ai

import (
	"encoding/json"
	"errors"
	"time"
)

// CurrentSchemaVersion is the version of the serializable AI message and
// context contracts. Zero remains accepted when decoding legacy values.
const CurrentSchemaVersion = 1

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
	ThinkingOff     ThinkingLevel = "off"
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
	ThinkingMax     ThinkingLevel = "max"
)

// Model describes a single provider model.
type Model struct {
	ID            string            `json:"id"`
	Name          string            `json:"name,omitempty"`
	API           API               `json:"api,omitempty"`
	Provider      ProviderID        `json:"provider,omitempty"`
	BaseURL       string            `json:"base_url,omitempty"`
	Reasoning     bool              `json:"reasoning,omitempty"`
	Input         []InputKind       `json:"input,omitempty"`
	Cost          Cost              `json:"cost,omitempty"`
	ContextWindow int64             `json:"context_window,omitempty"`
	MaxTokens     int64             `json:"max_tokens,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Metadata      json.RawMessage   `json:"metadata,omitempty"`
	Capabilities  ModelCapabilities `json:"capabilities,omitempty"`
}

// ModelCapabilities are provider/model features used by request validation.
// Keeping this type in ai avoids coupling model selection to a concrete
// provider package.
type ModelCapabilities struct {
	Vision           bool `json:"vision,omitempty"`
	Tools            bool `json:"tools,omitempty"`
	Reasoning        bool `json:"reasoning,omitempty"`
	PromptCache      bool `json:"prompt_cache,omitempty"`
	JSONMode         bool `json:"json_mode,omitempty"`
	StructuredOutput bool `json:"structured_output,omitempty"`
	Streaming        bool `json:"streaming,omitempty"`
}

// InputKind identifies model input modalities.
type InputKind string

const (
	InputText  InputKind = "text"
	InputImage InputKind = "image"
)

// Cost stores per-million-token costs in USD.
type Cost struct {
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

// Role identifies the source of a transcript message.
type Role string

const (
	RoleSystem     Role = "system"
	RoleDeveloper  Role = "developer"
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "tool_result"
)

// Message is the normalized transcript unit consumed by providers.
type Message struct {
	SchemaVersion    int             `json:"schema_version,omitempty"`
	ID               string          `json:"id,omitempty"`
	Role             Role            `json:"role"`
	Content          []ContentBlock  `json:"content,omitempty"`
	ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
	ToolResult       *ToolResult     `json:"tool_result,omitempty"`
	Timestamp        time.Time       `json:"timestamp,omitempty"`
	ResponseID       string          `json:"response_id,omitempty"`
	Provider         ProviderID      `json:"provider,omitempty"`
	ModelID          string          `json:"model_id,omitempty"`
	StopReason       StopReason      `json:"stop_reason,omitempty"`
	Usage            Usage           `json:"usage,omitempty"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	ProviderMetadata json.RawMessage `json:"provider_metadata,omitempty"`
	Details          json.RawMessage `json:"details,omitempty"`
	Error            *ProviderError  `json:"error,omitempty"`
}

// ContentBlock is a provider-neutral message content block.
type ContentBlock struct {
	Type             ContentType     `json:"type"`
	Text             string          `json:"text,omitempty"`
	Thinking         string          `json:"thinking,omitempty"`
	ThinkingRedacted bool            `json:"thinking_redacted,omitempty"`
	Signature        string          `json:"signature,omitempty"`
	ImageData        []byte          `json:"image_data,omitempty"`
	ImageMIMEType    string          `json:"image_mime_type,omitempty"`
	Details          json.RawMessage `json:"details,omitempty"`
	ProviderMetadata json.RawMessage `json:"provider_metadata,omitempty"`
}

// ContentType identifies the populated fields in a ContentBlock.
type ContentType string

const (
	ContentText       ContentType = "text"
	ContentThinking   ContentType = "thinking"
	ContentImage      ContentType = "image"
	ContentAudio      ContentType = "audio"
	ContentToolCall   ContentType = "tool_call"
	ContentToolResult ContentType = "tool_result"
)

// Tool declares a callable tool exposed to a provider.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

// ToolCall is a normalized provider request to invoke a tool.
type ToolCall struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Arguments        json.RawMessage `json:"arguments,omitempty"`
	ThoughtSignature string          `json:"thought_signature,omitempty"`
	Details          json.RawMessage `json:"details,omitempty"`
}

// ToolResult is a normalized result returned for a prior tool call.
type ToolResult struct {
	ToolCallID string          `json:"tool_call_id"`
	ToolName   string          `json:"tool_name,omitempty"`
	Content    []ContentBlock  `json:"content,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	Details    json.RawMessage `json:"details,omitempty"`
	Usage      Usage           `json:"usage,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	Logs       []LogEntry      `json:"logs,omitempty"`
}

// LogEntry is an optional structured diagnostic emitted during tool
// execution. It is preserved in transcripts but can be omitted by transports
// that do not need verbose logs.
type LogEntry struct {
	Timestamp time.Time       `json:"timestamp"`
	Level     string          `json:"level,omitempty"`
	Message   string          `json:"message"`
	Details   json.RawMessage `json:"details,omitempty"`
}

// Context is the provider input assembled by the agent loop.
type Context struct {
	SchemaVersion int             `json:"schema_version,omitempty"`
	SystemPrompt  string          `json:"system_prompt,omitempty"`
	Messages      []Message       `json:"messages,omitempty"`
	Tools         []Tool          `json:"tools,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

// Usage is the normalized accounting emitted by providers.
type Usage struct {
	InputTokens      int64     `json:"input_tokens,omitempty"`
	OutputTokens     int64     `json:"output_tokens,omitempty"`
	ReasoningTokens  int64     `json:"reasoning_tokens,omitempty"`
	CacheReadTokens  int64     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64     `json:"cache_write_tokens,omitempty"`
	TotalTokens      int64     `json:"total_tokens,omitempty"`
	Cost             UsageCost `json:"cost,omitempty"`
}

// UsageCost stores request cost components in USD.
type UsageCost struct {
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
	Total      float64 `json:"total,omitempty"`
}

// StopReason is the normalized reason a stream ended.
type StopReason string

const (
	StopReasonStop     StopReason = "stop"
	StopReasonLength   StopReason = "length"
	StopReasonToolUse  StopReason = "tool_use"
	StopReasonError    StopReason = "error"
	StopReasonAborted  StopReason = "aborted"
	StopReasonCanceled StopReason = "canceled"
	StopReasonDeferred StopReason = "deferred"
)

// ProviderError is a serializable provider failure preserved in transcripts.
// The original Go error is intentionally not included because it cannot be
// transported safely between workers.
type ProviderError struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
	Provider  string `json:"provider,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// EventKind identifies a normalized provider stream event.
type EventKind string

const (
	EventTextDelta     EventKind = "text_delta"
	EventThinkingDelta EventKind = "thinking_delta"
	EventImage         EventKind = "image"
	EventToolCall      EventKind = "tool_call"
	EventToolProgress  EventKind = "tool_progress"
	EventUsage         EventKind = "usage"
	EventStop          EventKind = "stop"
	EventError         EventKind = "error"
)

// Event is implemented by every normalized provider stream event.
type Event interface {
	Kind() EventKind
}

// TextDelta is a streamed assistant text fragment.
type TextDelta struct {
	ContentIndex int    `json:"content_index,omitempty"`
	Text         string `json:"text"`
}

func (TextDelta) Kind() EventKind { return EventTextDelta }

// ThinkingDelta is a native reasoning fragment. It must not be converted to
// ordinary assistant text by consumers.
type ThinkingDelta struct {
	ContentIndex int    `json:"content_index,omitempty"`
	Thinking     string `json:"thinking"`
	Signature    string `json:"signature,omitempty"`
}

func (ThinkingDelta) Kind() EventKind { return EventThinkingDelta }

// ImageEvent carries an image content block without relying on a provider's
// wire representation.
type ImageEvent struct {
	ContentIndex     int             `json:"content_index,omitempty"`
	Data             []byte          `json:"data,omitempty"`
	MIMEType         string          `json:"mime_type,omitempty"`
	ProviderMetadata json.RawMessage `json:"provider_metadata,omitempty"`
}

func (ImageEvent) Kind() EventKind { return EventImage }

// ToolCallEvent is a streamed tool call. ArgumentsDelta may contain partial
// JSON bytes while ToolCall.Arguments contains the complete arguments when
// Complete is true.
type ToolCallEvent struct {
	ContentIndex   int             `json:"content_index,omitempty"`
	ToolCall       ToolCall        `json:"tool_call"`
	ArgumentsDelta json.RawMessage `json:"arguments_delta,omitempty"`
	Complete       bool            `json:"complete,omitempty"`
}

func (ToolCallEvent) Kind() EventKind { return EventToolCall }

// UsageEvent reports normalized token and cost usage.
type UsageEvent struct {
	Usage Usage `json:"usage"`
}

func (UsageEvent) Kind() EventKind { return EventUsage }

// StopEvent reports normal or provider-requested stream termination.
type StopEvent struct {
	Reason           StopReason      `json:"reason"`
	ResponseID       string          `json:"response_id,omitempty"`
	ProviderMetadata json.RawMessage `json:"provider_metadata,omitempty"`
	Details          json.RawMessage `json:"details,omitempty"`
}

func (StopEvent) Kind() EventKind { return EventStop }

// ErrorEvent reports a provider-normalized stream error without forcing the
// stream transport itself to fail.
type ErrorEvent struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
	Err       error  `json:"-"`
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
