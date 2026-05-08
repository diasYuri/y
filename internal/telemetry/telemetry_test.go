package telemetry

import (
	"testing"
	"time"
)

func TestNewEvent(t *testing.T) {
	ev := NewEvent(EventAgentTurn, "sess-1", map[string]any{"turn": 1})
	if ev.Kind != EventAgentTurn {
		t.Fatalf("Kind = %q, want %q", ev.Kind, EventAgentTurn)
	}
	if ev.SessionID != "sess-1" {
		t.Fatalf("SessionID = %q, want sess-1", ev.SessionID)
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("Timestamp is zero")
	}
	if ev.Payload["turn"] != 1 {
		t.Fatalf("Payload[turn] = %v, want 1", ev.Payload["turn"])
	}
}

func TestNewEventNilPayload(t *testing.T) {
	ev := NewEvent(EventToolCall, "sess-2", nil)
	if ev.Payload == nil {
		t.Fatal("Payload is nil, want empty map")
	}
}

func TestAgentTurnPayload(t *testing.T) {
	p := AgentTurnPayload(3, "gpt-4", 100, 50)
	if p["turn"] != 3 {
		t.Fatalf("turn = %v, want 3", p["turn"])
	}
	if p["model_id"] != "gpt-4" {
		t.Fatalf("model_id = %v, want gpt-4", p["model_id"])
	}
	if p["input_tokens"] != int64(100) {
		t.Fatalf("input_tokens = %v, want 100", p["input_tokens"])
	}
	if p["output_tokens"] != int64(50) {
		t.Fatalf("output_tokens = %v, want 50", p["output_tokens"])
	}
}

func TestToolCallPayload(t *testing.T) {
	p := ToolCallPayload("read_file", 150, "")
	if p["tool_name"] != "read_file" {
		t.Fatalf("tool_name = %v, want read_file", p["tool_name"])
	}
	if p["duration_ms"] != int64(150) {
		t.Fatalf("duration_ms = %v, want 150", p["duration_ms"])
	}
	if _, ok := p["error"]; ok {
		t.Fatal("error should not be present for empty err")
	}
}

func TestToolCallPayloadWithError(t *testing.T) {
	p := ToolCallPayload("read_file", 150, "not found")
	if p["error"] != "not found" {
		t.Fatalf("error = %v, want not found", p["error"])
	}
}

func TestProviderRequestPayload(t *testing.T) {
	p := ProviderRequestPayload("openai", "gpt-4", 200, "")
	if p["provider_id"] != "openai" {
		t.Fatalf("provider_id = %v, want openai", p["provider_id"])
	}
	if p["model_id"] != "gpt-4" {
		t.Fatalf("model_id = %v, want gpt-4", p["model_id"])
	}
	if p["duration_ms"] != int64(200) {
		t.Fatalf("duration_ms = %v, want 200", p["duration_ms"])
	}
}

func TestNoopEmitter(t *testing.T) {
	var e NoopEmitter
	// Should not panic.
	e.Emit(NewEvent(EventAgentTurn, "sess", nil))
}

func TestBufferedEmitter(t *testing.T) {
	be := NewBufferedEmitter(4)
	be.Emit(NewEvent(EventAgentTurn, "sess-1", nil))
	be.Emit(NewEvent(EventToolCall, "sess-1", nil))

	if be.Len() != 2 {
		t.Fatalf("Len = %d, want 2", be.Len())
	}

	flushed := be.Flush()
	if len(flushed) != 2 {
		t.Fatalf("flushed length = %d, want 2", len(flushed))
	}
	if be.Len() != 0 {
		t.Fatalf("Len after flush = %d, want 0", be.Len())
	}

	// Second flush should return empty.
	flushed2 := be.Flush()
	if len(flushed2) != 0 {
		t.Fatalf("second flushed length = %d, want 0", len(flushed2))
	}
}

func TestBufferedEmitterPreservesOrder(t *testing.T) {
	be := NewBufferedEmitter(4)
	for i := 0; i < 5; i++ {
		be.Emit(NewEvent(EventAgentTurn, "sess", map[string]any{"idx": i}))
	}
	flushed := be.Flush()
	if len(flushed) != 5 {
		t.Fatalf("flushed length = %d, want 5", len(flushed))
	}
	for i, ev := range flushed {
		if ev.Payload["idx"] != i {
			t.Fatalf("event %d idx = %v, want %d", i, ev.Payload["idx"], i)
		}
	}
}

func TestDefaultEmitter(t *testing.T) {
	// DefaultEmitter should be safe to call without panic.
	DefaultEmitter.Emit(NewEvent(EventAgentTurn, "sess", nil))
}

func TestEventTimestampIsUTC(t *testing.T) {
	before := time.Now().UTC()
	ev := NewEvent(EventAgentTurn, "sess", nil)
	after := time.Now().UTC()

	if ev.Timestamp.Before(before) || ev.Timestamp.After(after) {
		t.Fatalf("timestamp %v not in range [%v, %v]", ev.Timestamp, before, after)
	}
}
