package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/policy"
	"github.com/diasYuri/y/pkg/providers"
	"github.com/diasYuri/y/pkg/tools"
)

func envelopeFromEvent(event Event, runID string, sequence uint64) (EventEnvelope, error) {
	eventType := event.Kind
	switch eventType {
	case EventTextDelta:
		eventType = EventMessageDelta
	case EventTurnEnded:
		eventType = EventTurnCompleted
	case EventToolEnded:
		eventType = EventToolCompleted
	case EventCompleted:
		eventType = EventAgentCompleted
	}
	toolCall := event.ToolCall
	toolCall.Arguments = policy.Redact(toolCall.Arguments)
	toolResult := redactedToolResult(event.ToolResult)
	message := cloneMessage(event.Message)
	for i := range message.ToolCalls {
		message.ToolCalls[i].Arguments = policy.Redact(message.ToolCalls[i].Arguments)
	}
	payload := map[string]any{
		"state": event.State, "turn": event.Turn, "message": message,
		"tool_call": toolCall, "tool_result": toolResult, "usage": event.Usage,
		"tool_progress": event.ToolProgress, "text_delta": event.TextDelta, "thinking_delta": event.ThinkingDelta,
	}
	if len(event.Payload) > 0 {
		payload["payload"] = json.RawMessage(append([]byte(nil), event.Payload...))
	}
	envelope, err := NewEventEnvelope(eventType, runID, sequence, payload)
	if err != nil {
		return EventEnvelope{}, err
	}
	envelope.SessionID = event.SessionID
	envelope.TurnID = event.TurnID
	envelope.ToolCallID = event.ToolCallID
	envelope.CorrelationID = event.CorrelationID
	envelope.IdempotencyKey = event.IdempotencyKey
	if event.Err != nil {
		envelope.Error = structuredEventError(event.Err)
	}
	return envelope, nil
}

func redactedToolResult(result ai.ToolResult) ai.ToolResult {
	raw, err := json.Marshal(result)
	if err != nil {
		return ai.ToolResult{}
	}
	redacted := policy.Redact(raw)
	if len(redacted) == 0 {
		return ai.ToolResult{}
	}
	if err := json.Unmarshal(redacted, &result); err != nil {
		return ai.ToolResult{}
	}
	return result
}

func structuredEventError(err error) *EventError {
	if err == nil {
		return nil
	}
	eventError := &EventError{Message: err.Error(), Retryable: isTransient(err)}
	var rateLimit *providers.RateLimitError
	var network *providers.NetworkError
	var authError *providers.AuthError
	var overflow *providers.ContextOverflowError
	switch {
	case errors.As(err, &rateLimit):
		eventError.Code = "rate_limit"
		eventError.Retryable = true
	case errors.As(err, &network):
		eventError.Code = "network"
		eventError.Retryable = network.StatusCode == 0 || network.StatusCode >= 500
	case errors.As(err, &authError):
		eventError.Code = "authentication"
		eventError.Retryable = false
	case errors.As(err, &overflow):
		eventError.Code = "context_overflow"
		eventError.Retryable = false
	case errors.Is(err, ErrRetry):
		eventError.Code = "retry_requested"
		eventError.Retryable = true
	}
	return eventError
}

// EventEnvelopeFromEvent converts the in-process event into the public wire
// envelope. The sequence is supplied by the owning runner/store.
func EventEnvelopeFromEvent(event Event, runID string, sequence uint64) (EventEnvelope, error) {
	return envelopeFromEvent(event, runID, sequence)
}

func cloneSnapshot(snapshot Snapshot) (Snapshot, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	var out Snapshot
	if err := json.Unmarshal(raw, &out); err != nil {
		return Snapshot{}, err
	}
	return out, nil
}

func cloneEvent(event EventEnvelope) EventEnvelope {
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	if event.Error != nil {
		copy := *event.Error
		event.Error = &copy
	}
	return event
}

func validateOperation(operation OperationRecord) error {
	if operation.SchemaVersion < 0 || operation.SchemaVersion > CurrentOperationSchemaVersion {
		return fmt.Errorf("%w: unsupported operation schema version %d", ErrInvalidEvent, operation.SchemaVersion)
	}
	if strings.TrimSpace(operation.IdempotencyKey) == "" {
		return fmt.Errorf("%w: operation idempotency key is required", ErrInvalidEvent)
	}
	if strings.TrimSpace(operation.ToolName) == "" {
		return fmt.Errorf("%w: operation tool name is required", ErrInvalidEvent)
	}
	if operation.State != "" && operation.State != OperationPending && operation.State != OperationCompleted && operation.State != OperationFailed {
		return fmt.Errorf("%w: invalid operation state %q", ErrInvalidEvent, operation.State)
	}
	return nil
}

func compareOperationIdentity(existing, requested OperationRecord) error {
	if existing.ArgumentsHash != requested.ArgumentsHash || existing.RunID != requested.RunID ||
		existing.TurnID != requested.TurnID || existing.ToolCallID != requested.ToolCallID ||
		existing.ToolName != requested.ToolName {
		return fmt.Errorf("%w: key %q", ErrIdempotencyConflict, requested.IdempotencyKey)
	}
	return nil
}

func cloneOperation(operation OperationRecord) OperationRecord {
	operation.Response = cloneToolResponsePtrValue(operation.Response)
	if operation.Error != nil {
		copy := *operation.Error
		operation.Error = &copy
	}
	return operation
}

func cloneToolResponsePtr(response tools.ToolResponse) *tools.ToolResponse {
	copy := cloneToolResponse(response)
	return &copy
}

func cloneToolResponsePtrValue(response *tools.ToolResponse) *tools.ToolResponse {
	if response == nil {
		return nil
	}
	return cloneToolResponsePtr(*response)
}

func cloneToolResponse(response tools.ToolResponse) tools.ToolResponse {
	copy := tools.ToolResponse{
		IsError:  response.IsError,
		Details:  append([]byte(nil), response.Details...),
		Metadata: append([]byte(nil), response.Metadata...),
		Logs:     append([]tools.LogEntry(nil), response.Logs...),
	}
	for i := range copy.Logs {
		copy.Logs[i].Details = append([]byte(nil), copy.Logs[i].Details...)
	}
	for _, block := range response.Content {
		copy.Content = append(copy.Content, tools.ContentBlock{
			Type:             block.Type,
			Text:             block.Text,
			ImageData:        append([]byte(nil), block.ImageData...),
			ImageMIMEType:    block.ImageMIMEType,
			Details:          append([]byte(nil), block.Details...),
			ProviderMetadata: append([]byte(nil), block.ProviderMetadata...),
		})
	}
	return copy
}

func structuredOperationError(err error) *EventError {
	if err == nil {
		return nil
	}
	return &EventError{Code: "operation_error", Message: err.Error(), Retryable: isTransient(err)}
}

func hasKey(events []EventEnvelope, key string) bool {
	for _, event := range events {
		if event.IdempotencyKey == key {
			return true
		}
	}
	return false
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("%d", time.Now().UTC().UnixNano())
}
