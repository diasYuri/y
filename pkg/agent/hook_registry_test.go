package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHookRegistrationRemovesAndDrainsCallbacks(t *testing.T) {
	registry := NewRuntimeHookRegistry()
	entered := make(chan struct{})
	release := make(chan struct{})
	registration, err := registry.Register(RuntimeHooks{BeforeTurn: func(context.Context, TurnContext) error {
		close(entered)
		<-release
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	agent := New(nil, nil, WithRuntimeHookRegistry(registry))
	hooks := agent.hooksSnapshot()
	callDone := make(chan error, 1)
	go func() { callDone <- hooks[0].BeforeTurn(context.Background(), TurnContext{}) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := registration.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first close error = %v, want deadline exceeded", err)
	}
	if got := agent.hooksSnapshot(); len(got) != 0 {
		t.Fatalf("hooks after close = %d, want none", len(got))
	}
	close(release)
	if err := <-callDone; err != nil {
		t.Fatalf("in-flight hook error = %v", err)
	}
	if err := registration.Close(context.Background()); err != nil {
		t.Fatalf("second close = %v", err)
	}
}
