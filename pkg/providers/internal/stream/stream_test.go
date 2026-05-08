package stream

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

func TestEventStreamSingleEvent(t *testing.T) {
	body := io.NopCloser(strings.NewReader("data: {\"text\":\"hello\"}\n\n"))
	consumer := func(data []byte) []ai.Event {
		return []ai.Event{ai.TextDelta{Text: string(data)}}
	}
	s := New(body, 1024, nil, "test", consumer)
	defer s.Close()

	event, err := s.Next(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	td, ok := event.(ai.TextDelta)
	if !ok {
		t.Fatalf("expected TextDelta, got %T", event)
	}
	if td.Text != `{"text":"hello"}` {
		t.Fatalf("text = %q", td.Text)
	}
}

func TestEventStreamMultipleEvents(t *testing.T) {
	body := io.NopCloser(strings.NewReader("data: a\n\ndata: b\n\n"))
	consumer := func(data []byte) []ai.Event {
		return []ai.Event{ai.TextDelta{Text: string(data)}}
	}
	s := New(body, 1024, nil, "test", consumer)
	defer s.Close()

	for _, want := range []string{"a", "b"} {
		event, err := s.Next(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		td := event.(ai.TextDelta)
		if td.Text != want {
			t.Fatalf("text = %q, want %q", td.Text, want)
		}
	}

	_, err := s.Next(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestEventStreamClose(t *testing.T) {
	body := io.NopCloser(strings.NewReader("data: a\n\n"))
	consumer := func(data []byte) []ai.Event {
		return []ai.Event{ai.TextDelta{Text: string(data)}}
	}
	s := New(body, 1024, nil, "test", consumer)

	if err := s.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	_, err := s.Next(context.Background())
	if !errors.Is(err, providers.ErrStreamClosed) {
		t.Fatalf("expected ErrStreamClosed, got %v", err)
	}
}

func TestEventStreamContextCancel(t *testing.T) {
	body := io.NopCloser(strings.NewReader(""))
	consumer := func(data []byte) []ai.Event { return nil }
	s := New(body, 1024, nil, "test", consumer)
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.Next(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestEventStreamDoneMarker(t *testing.T) {
	body := io.NopCloser(strings.NewReader("data: [DONE]\n\n"))
	consumer := func(data []byte) []ai.Event {
		return []ai.Event{ai.TextDelta{Text: string(data)}}
	}
	s := New(body, 1024, nil, "test", consumer)
	defer s.Close()

	_, err := s.Next(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after [DONE], got %v", err)
	}
}

func TestEventStreamConsumerError(t *testing.T) {
	body := io.NopCloser(strings.NewReader("data: bad\n\n"))
	consumer := func(data []byte) []ai.Event {
		return nil // no events for bad data
	}
	s := New(body, 1024, nil, "test", consumer)
	defer s.Close()

	_, err := s.Next(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after empty consumer, got %v", err)
	}
}

func TestEventStreamNilContext(t *testing.T) {
	body := io.NopCloser(strings.NewReader("data: hello\n\n"))
	consumer := func(data []byte) []ai.Event {
		return []ai.Event{ai.TextDelta{Text: string(data)}}
	}
	s := New(body, 1024, nil, "test", consumer)
	defer s.Close()

	event, err := s.Next(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event == nil {
		t.Fatal("expected event")
	}
}

func TestEventStreamMultipleResultsPerData(t *testing.T) {
	body := io.NopCloser(strings.NewReader("data: x\n\n"))
	consumer := func(data []byte) []ai.Event {
		return []ai.Event{
			ai.TextDelta{Text: "a"},
			ai.TextDelta{Text: "b"},
		}
	}
	s := New(body, 1024, nil, "test", consumer)
	defer s.Close()

	for _, want := range []string{"a", "b"} {
		event, err := s.Next(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		td := event.(ai.TextDelta)
		if td.Text != want {
			t.Fatalf("text = %q, want %q", td.Text, want)
		}
	}
}

func TestEventStreamCancelFunc(t *testing.T) {
	body := io.NopCloser(strings.NewReader("data: x\n\n"))
	consumer := func(data []byte) []ai.Event {
		return []ai.Event{ai.TextDelta{Text: string(data)}}
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := New(body, 1024, cancel, "test", consumer)

	// Read one event before closing.
	_, err := s.Next(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Close should call cancel.
	if err := s.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	// After close, Next should return ErrStreamClosed.
	_, err = s.Next(context.Background())
	if !errors.Is(err, providers.ErrStreamClosed) {
		t.Fatalf("expected ErrStreamClosed, got %v", err)
	}
}
