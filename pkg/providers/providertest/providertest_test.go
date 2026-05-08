package providertest_test

import (
	"context"
	"testing"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/providers/providertest"
)

func TestNewFakeProviderImplementsProvider(t *testing.T) {
	var _ providers.Provider = providertest.NewFakeProvider()
}

func TestFakeProviderRespondsToStream(t *testing.T) {
	p := providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{
		Events: []ai.Event{ai.TextDelta{Text: "hi"}, ai.StopEvent{Reason: ai.StopReasonStop}},
	}))
	defer p.Close()
	stream, err := p.Stream(context.Background(), providers.StreamRequest{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	ev, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.Kind() != ai.EventTextDelta {
		t.Fatalf("kind = %q, want text_delta", ev.Kind())
	}
}
