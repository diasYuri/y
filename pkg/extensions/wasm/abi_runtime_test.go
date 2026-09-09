package wasm

import (
	"encoding/json"
	"testing"
)

func TestRuntimeEnvelopeKindsRoundTrip(t *testing.T) {
	payload, err := json.Marshal(LifecycleRequest{Phase: "before_turn", RunID: "run"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := MarshalEnvelope(Envelope{Kind: KindLifecycle, Payload: payload}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Kind != KindLifecycle || envelope.APIVersion != SupportedAPIVersion {
		t.Fatalf("envelope = %#v", envelope)
	}
}
