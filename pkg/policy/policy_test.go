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

func TestEngineHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewEngine(DefaultConfig()).Decide(ctx, Request{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
