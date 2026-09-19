package providertest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/providers"
)

func TestFakeProviderStreamsQueuedEvents(t *testing.T) {
	events := []ai.Event{
		ai.TextDelta{Text: "hello"},
		ai.ToolCallEvent{
			ToolCall: ai.ToolCall{
				ID:        "call_1",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"path":"README.md"}`),
			},
			Complete: true,
		},
		ai.UsageEvent{Usage: ai.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}},
		ai.StopEvent{Reason: ai.StopReasonToolUse},
	}
	provider := NewFakeProvider(WithFakeResponses(FakeResponse{Events: events}))

	models, err := provider.Models(context.Background())
	if err != nil {
		t.Fatalf("Models returned error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "fake-1" {
		t.Fatalf("Models returned %#v, want default fake model", models)
	}

	stream, err := provider.Stream(context.Background(), providers.StreamRequest{Model: models[0]})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer stream.Close()

	for i, want := range events {
		got, err := stream.Next(context.Background())
		if err != nil {
			t.Fatalf("Next event %d returned error: %v", i, err)
		}
		if got.Kind() != want.Kind() {
			t.Fatalf("Next event %d kind = %q, want %q", i, got.Kind(), want.Kind())
		}
	}

	if _, err := stream.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Next after events returned %v, want io.EOF", err)
	}
	if provider.PendingResponseCount() != 0 {
		t.Fatalf("PendingResponseCount = %d, want 0", provider.PendingResponseCount())
	}
	if provider.CallCount() != 1 {
		t.Fatalf("CallCount = %d, want 1", provider.CallCount())
	}
}

func TestFakeStreamHonorsCancellation(t *testing.T) {
	provider := NewFakeProvider(WithFakeResponses(FakeResponse{
		Events: []ai.Event{ai.TextDelta{Text: "late"}},
		Delay:  time.Hour,
	}))
	stream, err := provider.Stream(context.Background(), providers.StreamRequest{})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer stream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	if _, err := stream.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next returned %v, want context deadline exceeded", err)
	}
}

func TestFakeStreamCloseUnblocksNext(t *testing.T) {
	provider := NewFakeProvider(WithFakeResponses(FakeResponse{
		Events: []ai.Event{ai.TextDelta{Text: "late"}},
		Delay:  time.Hour,
	}))
	stream, err := provider.Stream(context.Background(), providers.StreamRequest{})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := stream.Next(context.Background())
		errCh <- err
	}()

	time.Sleep(10 * time.Millisecond)
	if err := stream.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, providers.ErrStreamClosed) {
			t.Fatalf("blocked Next returned %v, want providers.ErrStreamClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock Next")
	}

	if _, err := stream.Next(context.Background()); !errors.Is(err, providers.ErrStreamClosed) {
		t.Fatalf("Next after Close returned %v, want providers.ErrStreamClosed", err)
	}
}

func TestFakeProviderCapabilitiesAndClose(t *testing.T) {
	p := NewFakeProvider()
	caps := p.Capabilities("fake-1")
	if !caps.Vision || !caps.Tools || !caps.Streaming {
		t.Fatalf("FakeProvider default caps = %+v, want Vision+Tools+Streaming", caps)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !p.IsClosed() {
		t.Fatal("FakeProvider IsClosed = false after Close")
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestFakeProviderCountTokensOverride(t *testing.T) {
	p := NewFakeProvider(WithFakeCountTokens(func(string, ai.Context) (int64, error) {
		return 999, nil
	}))
	got, err := p.CountTokens(context.Background(), "x", ai.Context{})
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if got != 999 {
		t.Fatalf("CountTokens = %d, want 999", got)
	}
}
