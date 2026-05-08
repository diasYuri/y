package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	policypkg "github.com/yuri/y/internal/policy"
)

func TestRegistryAddListAndHandle(t *testing.T) {
	reg := NewRegistry()
	err := reg.Add(ToolDescriptor{Name: "echo", Description: "Echo."}, ToolHandlerFunc(func(ctx context.Context, req ToolRequest) (ToolResponse, error) {
		return textResponse("ok", nil)
	}))
	if err != nil {
		t.Fatalf("Add returned error: %v", err)
	}
	if err := reg.Add(ToolDescriptor{Name: "echo"}, ToolHandlerFunc(func(context.Context, ToolRequest) (ToolResponse, error) {
		return ToolResponse{}, nil
	})); !errors.Is(err, ErrToolAlreadyRegistered) {
		t.Fatalf("duplicate Add error = %v, want ErrToolAlreadyRegistered", err)
	}

	list := reg.List()
	if len(list) != 1 || list[0].Name != "echo" {
		t.Fatalf("List = %#v, want echo descriptor", list)
	}
	resp, err := reg.Handle(context.Background(), ToolRequest{Name: "echo", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if got := resp.Content[0].Text; got != "ok" {
		t.Fatalf("response text = %q, want ok", got)
	}
	if _, err := reg.Handle(context.Background(), ToolRequest{Name: "missing"}); !errors.Is(err, ErrToolNotFound) {
		t.Fatalf("missing Handle error = %v, want ErrToolNotFound", err)
	}
}

func TestRegistryAppliesPolicyToSensitiveTools(t *testing.T) {
	reg := NewRegistry()
	ran := 0
	err := reg.Add(ToolDescriptor{Name: "write_file", Sensitive: true}, ToolHandlerFunc(func(ctx context.Context, req ToolRequest) (ToolResponse, error) {
		ran++
		return textResponse("ok", nil)
	}))
	if err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	_, err = reg.Handle(context.Background(), ToolRequest{Name: "write_file"})
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("Handle error = %v, want ErrApprovalRequired", err)
	}
	if ran != 0 {
		t.Fatalf("handler ran %d times, want 0", ran)
	}

	resp, err := reg.Handle(context.Background(), ToolRequest{
		Name: "write_file",
		Approval: &policypkg.ApprovalResolution{
			Mode:  policypkg.ApprovalModeHeadless,
			State: policypkg.ApprovalApproved,
		},
	})
	if err != nil {
		t.Fatalf("approved Handle returned error: %v", err)
	}
	if got := resp.Content[0].Text; got != "ok" {
		t.Fatalf("approved response text = %q, want ok", got)
	}
	if ran != 1 {
		t.Fatalf("handler ran %d times, want 1", ran)
	}

	_, err = reg.Handle(context.Background(), ToolRequest{
		Name: "write_file",
		Approval: &policypkg.ApprovalResolution{
			Mode:   policypkg.ApprovalModeHeadless,
			State:  policypkg.ApprovalDenied,
			Reason: "blocked by user",
		},
	})
	if !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("denied Handle error = %v, want ErrPolicyDenied", err)
	}
	if ran != 1 {
		t.Fatalf("handler ran %d times after deny, want 1", ran)
	}
}

func TestRegistryRequestsAndAppliesApprovalHandler(t *testing.T) {
	reg := NewRegistry(
		WithPolicy(policypkg.NewEngine(policypkg.Config{
			ApprovalMode:                policypkg.ApprovalModeHeadless,
			DenyEscapesWorkspace:        true,
			RequireApprovalForSensitive: true,
		})),
		WithApprovalHandler(ApprovalHandlerFunc(func(ctx context.Context, req policypkg.ApprovalRequest) (*policypkg.ApprovalResolution, error) {
			if req.Mode != policypkg.ApprovalModeHeadless {
				t.Fatalf("approval mode = %q, want %q", req.Mode, policypkg.ApprovalModeHeadless)
			}
			return &policypkg.ApprovalResolution{
				Mode:  policypkg.ApprovalModeHeadless,
				State: policypkg.ApprovalApproved,
				Actor: "user",
			}, nil
		})),
	)

	ran := 0
	if err := reg.Add(ToolDescriptor{Name: "write_file", Sensitive: true}, ToolHandlerFunc(func(ctx context.Context, req ToolRequest) (ToolResponse, error) {
		ran++
		if req.Approval == nil || req.Approval.State != policypkg.ApprovalApproved {
			t.Fatalf("tool approval = %#v, want approved", req.Approval)
		}
		return textResponse("ok", nil)
	})); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	resp, err := reg.Handle(context.Background(), ToolRequest{Name: "write_file"})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if got := resp.Content[0].Text; got != "ok" {
		t.Fatalf("response text = %q, want ok", got)
	}
	if ran != 1 {
		t.Fatalf("handler ran %d times, want 1", ran)
	}
}

func TestRegistryCache(t *testing.T) {
	calls := 0
	handler := ToolHandlerFunc(func(ctx context.Context, req ToolRequest) (ToolResponse, error) {
		calls++
		return textResponse("ok-"+string(req.Arguments), nil)
	})

	reg := NewRegistry(WithCache(1*time.Hour, 10))
	if err := reg.Add(ToolDescriptor{Name: "cached", Description: "Cached tool."}, handler); err != nil {
		t.Fatalf("Add error: %v", err)
	}

	// First call executes the handler.
	resp1, err := reg.Handle(context.Background(), ToolRequest{Name: "cached", Arguments: json.RawMessage(`{"x":1}`)})
	if err != nil {
		t.Fatalf("Handle error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}

	// Second call with same arguments returns cached result.
	resp2, err := reg.Handle(context.Background(), ToolRequest{Name: "cached", Arguments: json.RawMessage(`{"x":1}`)})
	if err != nil {
		t.Fatalf("Handle error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (cached)", calls)
	}
	if resp1.Content[0].Text != resp2.Content[0].Text {
		t.Fatalf("cached response differs: %q vs %q", resp1.Content[0].Text, resp2.Content[0].Text)
	}

	// Different arguments trigger new execution.
	_, err = reg.Handle(context.Background(), ToolRequest{Name: "cached", Arguments: json.RawMessage(`{"x":2}`)})
	if err != nil {
		t.Fatalf("Handle error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestRegistryCacheSkipsSensitiveTools(t *testing.T) {
	calls := 0
	handler := ToolHandlerFunc(func(ctx context.Context, req ToolRequest) (ToolResponse, error) {
		calls++
		return textResponse("ok", nil)
	})

	reg := NewRegistry(WithCache(1*time.Hour, 10))
	if err := reg.Add(ToolDescriptor{Name: "sensitive", Description: "Sensitive.", Sensitive: true}, handler); err != nil {
		t.Fatalf("Add error: %v", err)
	}

	approval := &policypkg.ApprovalResolution{State: policypkg.ApprovalApproved}
	for i := 0; i < 3; i++ {
		_, err := reg.Handle(context.Background(), ToolRequest{Name: "sensitive", Approval: approval})
		if err != nil {
			t.Fatalf("Handle error: %v", err)
		}
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (sensitive tools not cached)", calls)
	}
}

func TestRegistryCacheEviction(t *testing.T) {
	calls := 0
	handler := ToolHandlerFunc(func(ctx context.Context, req ToolRequest) (ToolResponse, error) {
		calls++
		return textResponse("ok", nil)
	})

	reg := NewRegistry(WithCache(1*time.Hour, 2))
	if err := reg.Add(ToolDescriptor{Name: "cached", Description: "Cached."}, handler); err != nil {
		t.Fatalf("Add error: %v", err)
	}

	// Fill cache to capacity.
	for i := 0; i < 2; i++ {
		reg.Handle(context.Background(), ToolRequest{Name: "cached", Arguments: json.RawMessage(fmt.Sprintf(`{"x":%d}`, i))})
	}

	// This should trigger eviction.
	reg.Handle(context.Background(), ToolRequest{Name: "cached", Arguments: json.RawMessage(`{"x":99}`)})

	// Re-execute first call (should have been evicted).
	reg.Handle(context.Background(), ToolRequest{Name: "cached", Arguments: json.RawMessage(`{"x":0}`)})
	if calls < 3 {
		t.Fatalf("calls = %d, want >= 3 (eviction should have occurred)", calls)
	}
}
