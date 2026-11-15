package tools

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestToolRegistrationRemovesAndDrains(t *testing.T) {
	registry := NewRegistry()
	entered := make(chan struct{})
	release := make(chan struct{})
	registration, err := registry.Register(ToolDescriptor{Name: "owned"}, ToolHandlerFunc(func(context.Context, ToolRequest) (ToolResponse, error) {
		close(entered)
		<-release
		return ToolResponse{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	callDone := make(chan error, 1)
	go func() {
		_, err := registry.Handle(context.Background(), ToolRequest{Name: "owned"})
		callDone <- err
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := registration.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first close error = %v, want deadline exceeded", err)
	}
	if got := registry.List(); len(got) != 0 {
		t.Fatalf("registered tools = %#v, want none", got)
	}
	if _, err := registry.Handle(context.Background(), ToolRequest{Name: "owned"}); !errors.Is(err, ErrToolNotFound) {
		t.Fatalf("new call error = %v, want tool not found", err)
	}
	close(release)
	if err := <-callDone; err != nil {
		t.Fatalf("in-flight call error = %v", err)
	}
	if err := registration.Close(context.Background()); err != nil {
		t.Fatalf("second close = %v", err)
	}
}
