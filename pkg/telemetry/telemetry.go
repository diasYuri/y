package telemetry

import "time"

// Emitter receives telemetry events.
type Emitter interface {
	Emit(Event)
}

// NoopEmitter discards all events.
type NoopEmitter struct{}

// Emit implements Emitter.
func (NoopEmitter) Emit(Event) {}

// BufferedEmitter batches events in memory until Flush is called.
type BufferedEmitter struct{ buffer []Event }

// NewBufferedEmitter creates a BufferedEmitter with the given capacity hint.
func NewBufferedEmitter(capacity int) *BufferedEmitter {
	return &BufferedEmitter{buffer: make([]Event, 0, capacity)}
}

// Emit appends an event to the buffer.
func (b *BufferedEmitter) Emit(event Event) {
	if b != nil {
		b.buffer = append(b.buffer, event)
	}
}

// Flush returns all buffered events and clears the buffer.
func (b *BufferedEmitter) Flush() []Event {
	if b == nil {
		return nil
	}
	out := b.buffer
	b.buffer = nil
	return out
}

// Len returns the number of buffered events.
func (b *BufferedEmitter) Len() int {
	if b == nil {
		return 0
	}
	return len(b.buffer)
}

// EventKind identifies the type of a telemetry event.
type EventKind string

const (
	EventAgentTurn       EventKind = "agent_turn"
	EventToolCall        EventKind = "tool_call"
	EventProviderRequest EventKind = "provider_request"
)

// Event is a single telemetry datum.
type Event struct {
	Kind      EventKind      `json:"kind"`
	Timestamp time.Time      `json:"timestamp"`
	SessionID string         `json:"session_id,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// NewEvent creates an Event with the current UTC timestamp.
func NewEvent(kind EventKind, sessionID string, payload map[string]any) Event {
	if payload == nil {
		payload = make(map[string]any)
	}
	return Event{Kind: kind, Timestamp: time.Now().UTC(), SessionID: sessionID, Payload: payload}
}

// AgentTurnPayload builds a payload for an agent turn event.
func AgentTurnPayload(turn int, modelID string, inputTokens, outputTokens int64) map[string]any {
	return map[string]any{"turn": turn, "model_id": modelID, "input_tokens": inputTokens, "output_tokens": outputTokens}
}

// ToolCallPayload builds a payload for a tool call event.
func ToolCallPayload(toolName string, durationMs int64, err string) map[string]any {
	payload := map[string]any{"tool_name": toolName, "duration_ms": durationMs}
	if err != "" {
		payload["error"] = err
	}
	return payload
}

// ProviderRequestPayload builds a payload for a provider request event.
func ProviderRequestPayload(providerID, modelID string, durationMs int64, err string) map[string]any {
	payload := map[string]any{"provider_id": providerID, "model_id": modelID, "duration_ms": durationMs}
	if err != "" {
		payload["error"] = err
	}
	return payload
}
