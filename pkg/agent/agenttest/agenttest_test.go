package agenttest_test

import (
	"context"
	"strings"
	"testing"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/agent/agenttest"
	"github.com/diasYuri/y/pkg/tools"
)

// TestScriptProvider_TwoTurnLoop runs a scripted agent loop: assistant
// requests a tool, the tool answers, the assistant finishes. It exercises
// every helper the package exports.
func TestScriptProvider_TwoTurnLoop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := agenttest.NewScriptProvider(t,
		agenttest.ToolTurn("read_file", `{"path":"main.go"}`),
		agenttest.TextTurn("The file defines main."),
	)

	registry := tools.NewRegistry()
	handler, recorded := agenttest.RecordingTool("package main\n\nfunc main() {}")
	agenttest.MustRegister(t, registry, tools.ToolDescriptor{Name: "read_file"}, handler)

	sink, events := agenttest.EventCollector()
	a := agenttest.NewTestAgent(t, provider, registry, agent.WithEventSink(sink))

	result, err := a.Run(ctx, "what does main.go do?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	agenttest.AssertState(t, result, agent.StateCompleted)
	agenttest.AssertToolCalls(t, result, "read_file")
	agenttest.AssertTranscriptRoles(t, a, "user", "assistant", "tool", "assistant")

	calls := recorded()
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if string(calls[0].Arguments) != `{"path":"main.go"}` {
		t.Fatalf("unexpected tool arguments: %s", calls[0].Arguments)
	}

	if len(provider.Requests()) != 2 {
		t.Fatalf("expected 2 provider requests, got %d", len(provider.Requests()))
	}
	if len(agenttest.EventKinds(events())) == 0 {
		t.Fatal("expected collected events")
	}
}

func TestScriptProvider_CapturesRequests(t *testing.T) {
	t.Parallel()
	provider := agenttest.NewScriptProvider(t,
		agenttest.TextTurn("hello"),
	)
	registry := tools.NewRegistry()
	a := agenttest.NewTestAgent(t, provider, registry)

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	requests := provider.Requests()
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Context.SystemPrompt != "agenttest" {
		t.Fatalf("expected system prompt forwarded, got %q", requests[0].Context.SystemPrompt)
	}
	if len(requests[0].Context.Messages) == 0 {
		t.Fatal("expected messages forwarded")
	}
}

func TestFakeTool_ReturnsFixedResult(t *testing.T) {
	t.Parallel()
	provider := agenttest.NewScriptProvider(t,
		agenttest.ToolTurn("noop", `{}`),
		agenttest.TextTurn("done"),
	)
	registry := tools.NewRegistry()
	agenttest.MustRegister(t, registry, tools.ToolDescriptor{Name: "noop"}, agenttest.FakeTool("fixed result"))

	a := agenttest.NewTestAgent(t, provider, registry)
	result, err := a.Run(context.Background(), "run noop")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	found := false
	for _, message := range result.Messages {
		if message.ToolResult != nil {
			for _, block := range message.ToolResult.Content {
				if strings.Contains(block.Text, "fixed result") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("expected fake tool result in transcript")
	}
}
