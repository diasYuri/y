package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/policy"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/providers/providertest"
	"github.com/yuri/y/pkg/telemetry"
	"github.com/yuri/y/pkg/tools"
)

func TestRuntimeHooksMutateMessageAndToolResult(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(
		providertest.FakeResponse{Events: []ai.Event{
			ai.ToolCallEvent{ContentIndex: 0, ToolCall: ai.ToolCall{ID: "call", Name: "echo", Arguments: json.RawMessage(`{}`)}, Complete: true},
			ai.StopEvent{Reason: ai.StopReasonToolUse},
		}},
		providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "done"}, ai.StopEvent{Reason: ai.StopReasonStop}}},
	))
	registry := tools.NewRegistry()
	if err := registry.Add(tools.ToolDescriptor{Name: "echo", InputSchema: []byte(`{"type":"object"}`)}, tools.ToolHandlerFunc(func(context.Context, tools.ToolRequest) (tools.ToolResponse, error) {
		return tools.ToolResponse{Content: []tools.ContentBlock{{Type: tools.ContentText, Text: "original"}}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	var sawMessage bool
	agent := New(provider, registry, WithRuntimeHooks(RuntimeHooks{
		BeforeMessage: func(_ context.Context, message *ai.Message) error {
			sawMessage = message.Content[0].Text == "hello"
			return nil
		},
		AfterTool: func(_ context.Context, _ ai.ToolCall, result *ai.ToolResult) error {
			result.Content[0].Text = "overridden"
			return nil
		},
	}))
	result, err := agent.Run(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !sawMessage {
		t.Fatal("BeforeMessage did not receive prompt")
	}
	var got string
	for _, message := range result.Messages {
		if message.ToolResult != nil {
			got = message.ToolResult.Content[0].Text
		}
	}
	if got != "overridden" {
		t.Fatalf("tool result = %q", got)
	}
}

func TestAgentAccountingRecordsProviderUsage(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{Events: []ai.Event{
		ai.TextDelta{Text: "ok"}, ai.UsageEvent{Usage: ai.Usage{InputTokens: 3, OutputTokens: 2}}, ai.StopEvent{Reason: ai.StopReasonStop},
	}}))
	accounting := telemetry.NewAccounting()
	agent := New(provider, tools.NewRegistry(), WithModel(ai.Model{ID: "test", Provider: "fake"}), WithRunID("run-1"), WithSessionID("session-1"), WithPolicyIdentity(policy.Identity{TenantID: "tenant-1"}), WithAccounting(accounting))
	if _, err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	snapshot := accounting.Snapshot()
	if snapshot.ByRun["run-1"].Usage.InputTokens != 3 || snapshot.BySession["session-1"].Usage.OutputTokens != 2 || snapshot.ByTenant["tenant-1"].Count == 0 {
		t.Fatalf("accounting = %#v", snapshot)
	}
}

func TestAgentCompactsAndRetriesProviderOverflow(t *testing.T) {
	seed := make([]ai.Message, 0, 9)
	for i := 0; i < 9; i++ {
		seed = append(seed, ai.Message{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: strings.Repeat("x", 400)}}})
	}
	provider := providertest.NewFakeProvider(
		providertest.WithFakeModels(ai.Model{ID: "test", Provider: "fake", ContextWindow: 1000}),
		providertest.WithFakeResponses(
			providertest.FakeResponse{Err: &providers.ContextOverflowError{Provider: "fake", StatusCode: 413}},
			providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "summary"}, ai.StopEvent{Reason: ai.StopReasonStop}}},
			providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "recovered"}, ai.StopEvent{Reason: ai.StopReasonStop}}},
		),
	)
	agent := New(provider, tools.NewRegistry(), WithModel(ai.Model{ID: "test", Provider: "fake", ContextWindow: 1000}), WithTranscript(seed...), WithCompaction(true))
	result, err := agent.Run(context.Background(), "trigger")
	if err != nil {
		t.Fatal(err)
	}
	if provider.CallCount() != 3 || result.Messages[len(result.Messages)-1].Content[0].Text != "recovered" {
		t.Fatalf("calls=%d result=%#v", provider.CallCount(), result)
	}
}

func TestAfterRunReceivesCompleteTranscriptBestEffort(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{Events: []ai.Event{ai.TextDelta{Text: "answer"}, ai.StopEvent{Reason: ai.StopReasonStop}}}))
	var got RunResult
	called := make(chan struct{}, 1)
	agent := New(provider, tools.NewRegistry(), WithRuntimeHooks(RuntimeHooks{AfterRun: func(_ context.Context, result RunResult) error {
		got = result
		called <- struct{}{}
		return errors.New("diagnostic only")
	}}))
	result, err := agent.Run(context.Background(), "question")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("AfterRun was not called")
	}
	if len(got.Messages) != len(result.Messages) || got.Messages[len(got.Messages)-1].Role != ai.RoleAssistant {
		t.Fatalf("AfterRun result = %#v", got)
	}
}
