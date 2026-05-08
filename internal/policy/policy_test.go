package policy

import (
	"context"
	"errors"
	"testing"
)

func TestEngineAllowsNonSensitiveOperation(t *testing.T) {
	eng := NewEngine(DefaultConfig())
	decision, err := eng.Decide(context.Background(), Request{
		ToolName:      "read_file",
		Capability:    "filesystem.read",
		WorkspaceRoot: "/workspace",
		Path:          "notes.txt",
	})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if decision.Kind != DecisionAllow {
		t.Fatalf("decision kind = %q, want allow", decision.Kind)
	}
}

func TestEngineDeniesWorkspaceEscape(t *testing.T) {
	eng := NewEngine(DefaultConfig())
	decision, err := eng.Decide(context.Background(), Request{
		ToolName:         "read_file",
		Capability:       "filesystem.read",
		WorkspaceRoot:    "/workspace",
		Path:             "../secret.txt",
		EscapesWorkspace: true,
	})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if decision.Kind != DecisionDeny {
		t.Fatalf("decision kind = %q, want deny", decision.Kind)
	}
}

func TestEngineRequiresApprovalForSensitiveOperation(t *testing.T) {
	eng := NewEngine(DefaultConfig())
	decision, err := eng.Decide(context.Background(), Request{
		ToolName:      "write_file",
		Capability:    "filesystem.write",
		WorkspaceRoot: "/workspace",
		Path:          "notes.txt",
		Sensitive:     true,
	})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if decision.Kind != DecisionRequireApproval {
		t.Fatalf("decision kind = %q, want require_approval", decision.Kind)
	}
	if decision.Approval == nil {
		t.Fatalf("decision approval = nil, want request metadata")
	}
	if decision.Approval.Mode != ApprovalModeHeadless {
		t.Fatalf("approval mode = %q, want headless", decision.Approval.Mode)
	}
}

func TestEngineHonorsApprovedResolution(t *testing.T) {
	eng := NewEngine(DefaultConfig())
	decision, err := eng.Decide(context.Background(), Request{
		ToolName:      "write_file",
		Capability:    "filesystem.write",
		WorkspaceRoot: "/workspace",
		Path:          "notes.txt",
		Sensitive:     true,
		Approval: &ApprovalResolution{
			Mode:  ApprovalModeHeadless,
			State: ApprovalApproved,
		},
	})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if decision.Kind != DecisionAllow {
		t.Fatalf("decision kind = %q, want allow", decision.Kind)
	}
}

func TestEngineHonorsDeniedResolution(t *testing.T) {
	eng := NewEngine(DefaultConfig())
	decision, err := eng.Decide(context.Background(), Request{
		ToolName:      "write_file",
		Capability:    "filesystem.write",
		WorkspaceRoot: "/workspace",
		Path:          "notes.txt",
		Sensitive:     true,
		Approval: &ApprovalResolution{
			Mode:   ApprovalModeHeadless,
			State:  ApprovalDenied,
			Reason: "blocked by user",
		},
	})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if decision.Kind != DecisionDeny {
		t.Fatalf("decision kind = %q, want deny", decision.Kind)
	}
	if decision.Reason != "blocked by user" {
		t.Fatalf("decision reason = %q, want user reason", decision.Reason)
	}
}

func TestFuncAdapts(t *testing.T) {
	eng := Func(func(ctx context.Context, req Request) (Decision, error) {
		return Decision{Kind: DecisionAllow}, nil
	})
	decision, err := eng.Decide(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if decision.Kind != DecisionAllow {
		t.Fatalf("decision kind = %q, want allow", decision.Kind)
	}
}

func TestEngineRejectsCanceledContext(t *testing.T) {
	eng := NewEngine(DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := eng.Decide(ctx, Request{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Decide error = %v, want context.Canceled", err)
	}
}
