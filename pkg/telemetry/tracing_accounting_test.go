package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yuri/y/pkg/ai"
)

func TestMemoryTracerPreservesParentAndEvents(t *testing.T) {
	tracer := NewMemoryTracer()
	ctx, root := tracer.Start(context.Background(), "agent.run", Attribute{Key: "run.id", Value: "r1"})
	_, child := tracer.Start(ctx, "provider.request")
	child.AddEvent("first_token")
	child.RecordError(errors.New("network"))
	child.End()
	root.End()
	spans := tracer.Spans()
	if len(spans) != 2 || spans[1].ParentID != spans[0].ID || spans[1].Status != StatusError || len(spans[1].Events) != 1 {
		t.Fatalf("Spans() = %#v", spans)
	}
}

func TestAccountingAggregatesEveryDimension(t *testing.T) {
	accounting := NewAccounting()
	accounting.Record(Measurement{
		Dimensions: Dimensions{RunID: "run", TurnID: "turn", SessionID: "session", ProviderID: "openai", ModelID: "gpt", ToolName: "read", TenantID: "tenant"},
		Usage:      ai.Usage{InputTokens: 4, OutputTokens: 6, ReasoningTokens: 2, Cost: ai.UsageCost{Total: 0.1}},
		Cost:       0.1, Latency: time.Second, TimeToFirstToken: time.Millisecond, ToolDuration: time.Millisecond,
		EstimatedContextTokens: 10, Retries: 1, Compactions: 1, Aborts: 1, ProviderErrors: 1,
	})
	snapshot := accounting.Snapshot()
	if snapshot.Total.Usage.InputTokens != 4 || snapshot.ByRun["run"].Usage.OutputTokens != 6 || snapshot.ByTenant["tenant"].Compactions != 1 || snapshot.ByTool["read"].ToolDuration != time.Millisecond {
		t.Fatalf("Snapshot() = %#v", snapshot)
	}
}
