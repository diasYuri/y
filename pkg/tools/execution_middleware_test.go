package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestExecutionMiddlewareRunsAfterPolicyAndSchemaValidation(t *testing.T) {
	policyCalls := 0
	reg := NewRegistry(WithPolicy(PolicyFunc(func(context.Context, PolicyRequest) (PolicyDecision, error) {
		policyCalls++
		return PolicyDecision{Kind: DecisionDeny}, nil
	})))
	handlerCalls, middlewareCalls := 0, 0
	if err := reg.Add(ToolDescriptor{Name: "protected", Sensitive: true, InputSchema: json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string"}}}`)}, ToolHandlerFunc(func(context.Context, ToolRequest) (ToolResponse, error) {
		handlerCalls++
		return ToolResponse{}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := reg.AddExecutionMiddleware(func(ctx context.Context, request ToolRequest, _ ToolDescriptor, next ToolExecutionNext) (ToolResponse, error) {
		middlewareCalls++
		return next(ctx, request)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Handle(context.Background(), ToolRequest{Name: "protected", Arguments: json.RawMessage(`{"path":"a.go"}`)}); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("policy denial = %v, want ErrPolicyDenied", err)
	}
	if policyCalls != 1 || middlewareCalls != 0 || handlerCalls != 0 {
		t.Fatalf("denied execution reached later layers: policy=%d middleware=%d handler=%d", policyCalls, middlewareCalls, handlerCalls)
	}

	allowed := NewRegistry()
	if err := allowed.Add(ToolDescriptor{Name: "validated", InputSchema: json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string"}}}`)}, ToolHandlerFunc(func(context.Context, ToolRequest) (ToolResponse, error) {
		handlerCalls++
		return ToolResponse{}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := allowed.AddExecutionMiddleware(func(ctx context.Context, request ToolRequest, _ ToolDescriptor, next ToolExecutionNext) (ToolResponse, error) {
		middlewareCalls++
		return next(ctx, request)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := allowed.Handle(context.Background(), ToolRequest{Name: "validated", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("invalid arguments were accepted")
	}
	if middlewareCalls != 0 || handlerCalls != 0 {
		t.Fatalf("invalid arguments reached later layers: middleware=%d handler=%d", middlewareCalls, handlerCalls)
	}
}

func TestExecutionMiddlewareCanReplayAndDisablesLocalCache(t *testing.T) {
	reg := NewRegistry(WithCache(time.Minute, 10))
	handlerCalls := 0
	if err := reg.Add(ToolDescriptor{Name: "read"}, ToolHandlerFunc(func(context.Context, ToolRequest) (ToolResponse, error) {
		handlerCalls++
		return ToolResponse{Content: []ContentBlock{{Type: ContentText, Text: "live"}}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	replay := false
	if err := reg.AddExecutionMiddleware(func(ctx context.Context, request ToolRequest, _ ToolDescriptor, next ToolExecutionNext) (ToolResponse, error) {
		if replay {
			return ToolResponse{Content: []ContentBlock{{Type: ContentText, Text: "confirmed"}}}, nil
		}
		return next(ctx, request)
	}); err != nil {
		t.Fatal(err)
	}
	request := ToolRequest{Name: "read", Arguments: json.RawMessage(`{}`)}
	first, err := reg.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	replay = true
	second, err := reg.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Content[0].Text != "live" || second.Content[0].Text != "confirmed" || handlerCalls != 1 {
		t.Fatalf("middleware result/cache mismatch: first=%+v second=%+v handlerCalls=%d", first, second, handlerCalls)
	}

	replay = false
	if _, err := reg.Handle(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if handlerCalls != 2 {
		t.Fatalf("middleware execution used process-local cache: handlerCalls=%d, want 2", handlerCalls)
	}
}
