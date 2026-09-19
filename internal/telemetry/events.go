package telemetry

import publictelemetry "github.com/diasYuri/y/pkg/telemetry"

type EventKind = publictelemetry.EventKind
type Event = publictelemetry.Event

const (
	EventAgentTurn       = publictelemetry.EventAgentTurn
	EventToolCall        = publictelemetry.EventToolCall
	EventProviderRequest = publictelemetry.EventProviderRequest
)

func NewEvent(kind EventKind, sessionID string, payload map[string]any) Event {
	return publictelemetry.NewEvent(kind, sessionID, payload)
}

func AgentTurnPayload(turn int, modelID string, inputTokens, outputTokens int64) map[string]any {
	return publictelemetry.AgentTurnPayload(turn, modelID, inputTokens, outputTokens)
}

func ToolCallPayload(toolName string, durationMs int64, err string) map[string]any {
	return publictelemetry.ToolCallPayload(toolName, durationMs, err)
}

func ProviderRequestPayload(providerID, modelID string, durationMs int64, err string) map[string]any {
	return publictelemetry.ProviderRequestPayload(providerID, modelID, durationMs, err)
}
