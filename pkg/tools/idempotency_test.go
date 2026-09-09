package tools

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRegistryIdempotencySerializesConcurrentSideEffects(t *testing.T) {
	registry := NewRegistry()
	var mu sync.Mutex
	calls := 0
	started := make(chan struct{})
	release := make(chan struct{})
	if err := registry.Add(ToolDescriptor{Name: "side_effect"}, ToolHandlerFunc(func(ctx context.Context, _ ToolRequest) (ToolResponse, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		close(started)
		select {
		case <-release:
			return ToolResponse{Content: []ContentBlock{{Type: ContentText, Text: "done"}}}, nil
		case <-ctx.Done():
			return ToolResponse{}, ctx.Err()
		}
	})); err != nil {
		t.Fatal(err)
	}

	results := make(chan error, 2)
	request := ToolRequest{Name: "side_effect", IdempotencyKey: "run-1-tool-1"}
	go func() {
		_, err := registry.Handle(context.Background(), request)
		results <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	go func() {
		_, err := registry.Handle(context.Background(), request)
		results <- err
	}()
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	if calls != 1 {
		t.Fatalf("handler calls while duplicate is waiting = %d, want 1", calls)
	}
	mu.Unlock()
	close(release)
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if err := <-results; err != nil {
		t.Fatal(err)
	}
}
