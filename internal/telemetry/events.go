package telemetry

import (
	"time"
)

// EventKind identifies the type of a telemetry event.
type EventKind string

const (
	// EventAgentTurn records a completed agent turn.
	EventAgentTurn EventKind = "agent_turn"
	// EventToolCall records a tool invocation.
	EventToolCall EventKind = "tool_call"
	// EventProviderRequest records a provider API request.
	EventProviderRequest EventKind = "provider_request"
)

// Event is a single telemetry datum.
type Event struct {
	// Kind identifies the event type.
	Kind EventKind
	// Timestamp is when the event occurred.
	Timestamp time.Time
	// SessionID identifies the logical session.
	SessionID string
	// Payload contains event-specific key/value data.
	Payload map[string]any
}

// NewEvent creates an Event with the current timestamp.
func NewEvent(kind EventKind, sessionID string, payload map[string]any) Event {
	if payload == nil {
		payload = make(map[string]any)
	}
	return Event{
		Kind:      kind,
		Timestamp: time.Now().UTC(),
		SessionID: sessionID,
		Payload:   payload,
	}
}

// AgentTurnPayload builds a payload for an agent turn event.
func AgentTurnPayload(turn int, modelID string, inputTokens, outputTokens int64) map[string]any {
	return map[string]any{
		"turn":          turn,
		"model_id":      modelID,
		"input_tokens":  inputTokens,
		"output_tokens": outputTokens,
	}
}

// ToolCallPayload builds a payload for a tool call event.
func ToolCallPayload(toolName string, durationMs int64, err string) map[string]any {
	payload := map[string]any{
		"tool_name":   toolName,
		"duration_ms": durationMs,
	}
	if err != "" {
		payload["error"] = err
	}
	return payload
}

// ProviderRequestPayload builds a payload for a provider request event.
func ProviderRequestPayload(providerID, modelID string, durationMs int64, err string) map[string]any {
	payload := map[string]any{
		"provider_id": providerID,
		"model_id":    modelID,
		"duration_ms": durationMs,
	}
	if err != "" {
		payload["error"] = err
	}
	return payload
}
