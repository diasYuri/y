package context

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type blockingContextSource struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (s *blockingContextSource) Name() string                                     { return "blocking" }
func (s *blockingContextSource) Version(context.Context, Request) (string, error) { return "1", nil }
func (s *blockingContextSource) Load(context.Context, Request) ([]Item, error) {
	s.calls.Add(1)
	close(s.entered)
	<-s.release
	return []Item{{ID: "item", Content: "value", Scope: ScopeGlobal}}, nil
}

func TestSourceRegistrationRemovesAndDrains(t *testing.T) {
	resolver := NewResolver()
	source := &blockingContextSource{entered: make(chan struct{}), release: make(chan struct{})}
	registration, err := resolver.Register(source)
	if err != nil {
		t.Fatal(err)
	}
	resolveDone := make(chan error, 1)
	go func() {
		_, err := resolver.Resolve(context.Background(), Request{})
		resolveDone <- err
	}()
	<-source.entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := registration.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first close error = %v, want deadline exceeded", err)
	}
	if got := resolver.Sources(); len(got) != 0 {
		t.Fatalf("sources after close = %d, want none", len(got))
	}
	close(source.release)
	if err := <-resolveDone; err != nil {
		t.Fatalf("in-flight resolve error = %v", err)
	}
	if err := registration.Close(context.Background()); err != nil {
		t.Fatalf("second close = %v", err)
	}
	if got := source.calls.Load(); got != 1 {
		t.Fatalf("source calls = %d, want one", got)
	}
}
