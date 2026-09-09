package policy

import (
	"context"
	"errors"
	"testing"
)

func TestEngineRequiresApprovalForSensitiveRequests(t *testing.T) {
	decision, err := NewEngine(DefaultConfig()).Decide(context.Background(), Request{
		ToolName:  "write_file",
		Sensitive: true,
	})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if decision.Kind != DecisionRequireApproval || decision.Approval == nil {
		t.Fatalf("decision = %+v, want approval", decision)
	}
}

func TestDistributedQuotaStoreIsIdempotentAcrossEngineInstances(t *testing.T) {
	quota := NewInMemoryQuotaStore()
	config := DistributedConfig{Config: Config{}, TenantLimits: map[string]int{"tenant-1": 1}, Quota: quota}
	first := NewDistributedEngine(config)
	second := NewDistributedEngine(config)
	request := Request{ToolName: "write", RequestID: "req-1", RunID: "run-1", TurnID: "turn-1", ToolCallID: "call-1", Identity: Identity{TenantID: "tenant-1"}}
	if _, err := first.Decide(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Decide(context.Background(), request); err != nil {
		t.Fatalf("same operation should be idempotent: %v", err)
	}
	decision, err := second.Decide(context.Background(), Request{ToolName: "write", RequestID: "req-2", RunID: "run-2", TurnID: "turn-1", ToolCallID: "call-1", Identity: Identity{TenantID: "tenant-1"}})
	if err != nil {
		t.Fatal(err)
	}
	// Quota denials are represented in the decision, rather than as a transport error.
	if decision.Kind != DecisionDeny {
		t.Fatalf("quota decision = %#v", decision)
	}
}

func TestEngineHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewEngine(DefaultConfig()).Decide(ctx, Request{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
