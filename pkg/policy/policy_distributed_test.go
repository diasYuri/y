package policy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDistributedPolicyChecksCapabilitiesAndRedactsAudit(t *testing.T) {
	audit := NewInMemoryAuditSink()
	engine := NewDistributedEngine(DistributedConfig{
		Config:              DefaultConfig(),
		Audit:               audit,
		AllowedCapabilities: map[string][]string{"tenant-a": {"filesystem.read"}},
	})
	req := Request{
		ToolName:             "read_file",
		Identity:             Identity{CallerID: "caller", TenantID: "tenant-a", Capabilities: []string{"filesystem.read"}},
		RequiredCapabilities: []string{"filesystem.read"},
		Arguments:            json.RawMessage(`{"token":"secret","path":"/tmp/a"}`),
	}
	decision, err := engine.Decide(context.Background(), req)
	if err != nil || decision.Kind != DecisionAllow {
		t.Fatalf("decision = %#v, err=%v", decision, err)
	}
	events := audit.Events()
	if len(events) != 1 || strings.Contains(string(events[0].Arguments), "secret") || events[0].ArgumentsHash == "" {
		t.Fatalf("audit events = %#v", events)
	}

	req.RequiredCapabilities = []string{"filesystem.write"}
	decision, err = engine.Decide(context.Background(), req)
	if err != nil || decision.Kind != DecisionDeny {
		t.Fatalf("denied decision = %#v, err=%v", decision, err)
	}
}

func TestDistributedPolicyHashesInvalidArgumentsWithoutPersistingThem(t *testing.T) {
	audit := NewInMemoryAuditSink()
	engine := NewDistributedEngine(DistributedConfig{
		Config: DefaultConfig(),
		Audit:  audit,
	})
	req := Request{
		ToolName:  "send_secret",
		Identity:  Identity{TenantID: "tenant-a"},
		Arguments: []byte("token=invalid-json-secret"),
	}
	if _, err := engine.Decide(context.Background(), req); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	events := audit.Events()
	if len(events) != 1 {
		t.Fatalf("audit event count = %d, want 1", len(events))
	}
	if len(events[0].Arguments) != 0 || events[0].ArgumentsHash == "" {
		t.Fatalf("audit event = %#v, want hash-only arguments", events[0])
	}
	if strings.Contains(string(events[0].Arguments), "invalid-json-secret") {
		t.Fatalf("audit event leaked invalid arguments: %#v", events[0])
	}
}
