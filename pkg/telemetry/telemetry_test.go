package telemetry

import "testing"

func TestBufferedEmitterRoundTrip(t *testing.T) {
	emitter := NewBufferedEmitter(1)
	emitter.Emit(NewEvent(EventToolCall, "session", ToolCallPayload("read_file", 4, "")))
	if emitter.Len() != 1 {
		t.Fatalf("Len = %d, want 1", emitter.Len())
	}
	events := emitter.Flush()
	if len(events) != 1 || events[0].SessionID != "session" {
		t.Fatalf("events = %+v, want one session event", events)
	}
	if emitter.Len() != 0 {
		t.Fatalf("Len after Flush = %d, want 0", emitter.Len())
	}
}
