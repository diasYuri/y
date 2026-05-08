package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/tools"
)

func TestAgentRunSimpleResponse(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "hello world"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))
	registry := tools.NewRegistry()

	agent := New(provider, registry)
	result, err := agent.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if result.State != StateCompleted {
		t.Fatalf("result state = %q, want %q", result.State, StateCompleted)
	}
	if result.Turns != 1 {
		t.Fatalf("result turns = %d, want 1", result.Turns)
	}
	if got := result.StopReason; got != ai.StopReasonStop {
		t.Fatalf("stop reason = %q, want %q", got, ai.StopReasonStop)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("messages length = %d, want 2", len(result.Messages))
	}
	if got := result.Messages[0].Role; got != ai.RoleUser {
		t.Fatalf("first message role = %q, want user", got)
	}
	if got := result.Messages[1].Role; got != ai.RoleAssistant {
		t.Fatalf("second message role = %q, want assistant", got)
	}
	if len(result.Messages[1].Content) != 1 || result.Messages[1].Content[0].Text != "hello world" {
		t.Fatalf("assistant content = %#v, want hello world", result.Messages[1].Content)
	}
	if len(result.Messages[1].ToolCalls) != 0 {
		t.Fatalf("assistant tool calls = %#v, want none", result.Messages[1].ToolCalls)
	}
	if got := provider.CallCount(); got != 1 {
		t.Fatalf("provider call count = %d, want 1", got)
	}
}

func TestAgentRunExecutesToolCallsIteratively(t *testing.T) {
	provider := &capturingProvider{
		FakeProvider: providers.NewFakeProvider(providers.WithFakeResponses(
			providers.FakeResponse{
				Events: []ai.Event{
					ai.ToolCallEvent{
						ContentIndex: 0,
						ToolCall: ai.ToolCall{
							ID:        "call_1",
							Name:      "echo",
							Arguments: json.RawMessage(`{"text":"from model"}`),
						},
						Complete: true,
					},
					ai.StopEvent{Reason: ai.StopReasonToolUse},
				},
			},
			providers.FakeResponse{
				Events: []ai.Event{
					ai.TextDelta{Text: "done"},
					ai.StopEvent{Reason: ai.StopReasonStop},
				},
			},
		)),
	}

	registry := tools.NewRegistry()
	var toolCalls int
	if err := registry.Add(tools.ToolDescriptor{
		Name:        "echo",
		Description: "Echo input text.",
	}, tools.ToolHandlerFunc(func(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
		toolCalls++
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(req.Arguments, &payload); err != nil {
			return tools.ToolResponse{}, err
		}
		return tools.ToolResponse{
			Content: []tools.ContentBlock{{Type: tools.ContentText, Text: "tool:" + payload.Text}},
		}, nil
	})); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	agent := New(provider, registry, WithWorkspaceRoot("/workspace"))
	result, err := agent.Run(context.Background(), "start")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if result.State != StateCompleted {
		t.Fatalf("result state = %q, want %q", result.State, StateCompleted)
	}
	if result.Turns != 2 {
		t.Fatalf("result turns = %d, want 2", result.Turns)
	}
	if got := result.StopReason; got != ai.StopReasonStop {
		t.Fatalf("stop reason = %q, want %q", got, ai.StopReasonStop)
	}
	if len(result.Messages) != 4 {
		t.Fatalf("messages length = %d, want 4", len(result.Messages))
	}

	assistantWithTool := result.Messages[1]
	if len(assistantWithTool.ToolCalls) != 1 {
		t.Fatalf("tool call count = %d, want 1", len(assistantWithTool.ToolCalls))
	}
	if got := assistantWithTool.ToolCalls[0].Name; got != "echo" {
		t.Fatalf("tool call name = %q, want echo", got)
	}

	toolResult := result.Messages[2]
	if toolResult.Role != ai.RoleToolResult || toolResult.ToolResult == nil {
		t.Fatalf("tool result message = %#v, want tool result", toolResult)
	}
	if got := toolResult.ToolResult.ToolName; got != "echo" {
		t.Fatalf("tool result tool name = %q, want echo", got)
	}
	if len(toolResult.ToolResult.Content) != 1 || toolResult.ToolResult.Content[0].Text != "tool:from model" {
		t.Fatalf("tool result content = %#v, want tool:from model", toolResult.ToolResult.Content)
	}

	if toolCalls != 1 {
		t.Fatalf("tool call count = %d, want 1", toolCalls)
	}

	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("provider request count = %d, want 2", len(requests))
	}
	if got := len(requests[0].Context.Tools); got != 1 {
		t.Fatalf("first request tools = %d, want 1", got)
	}
	if got := requests[0].Context.Tools[0].Name; got != "echo" {
		t.Fatalf("first request tool name = %q, want echo", got)
	}
	if got := len(requests[0].Context.Messages); got != 1 {
		t.Fatalf("first request message count = %d, want 1", got)
	}
	if got := len(requests[1].Context.Messages); got != 3 {
		t.Fatalf("second request message count = %d, want 3", got)
	}
	if got := requests[1].Context.Messages[1].Role; got != ai.RoleAssistant {
		t.Fatalf("second request message[1] role = %q, want assistant", got)
	}
	if got := requests[1].Context.Messages[2].Role; got != ai.RoleToolResult {
		t.Fatalf("second request message[2] role = %q, want tool_result", got)
	}
}

func TestAgentCancellationInterruptsProvider(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{ai.TextDelta{Text: "late"}},
		Delay:  time.Hour,
	}))
	agent := New(provider, tools.NewRegistry())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := agent.Run(ctx, "hello")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Run took %s to cancel", elapsed)
	}
	if got := agent.State(); got != StateCanceled {
		t.Fatalf("agent state = %q, want %q", got, StateCanceled)
	}
}

func TestAgentCancellationInterruptsTool(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.ToolCallEvent{
				ContentIndex: 0,
				ToolCall: ai.ToolCall{
					ID:        "call_1",
					Name:      "wait",
					Arguments: json.RawMessage(`{"value":"x"}`),
				},
				Complete: true,
			},
			ai.StopEvent{Reason: ai.StopReasonToolUse},
		},
	}))
	registry := tools.NewRegistry()
	toolStarted := make(chan struct{}, 1)
	if err := registry.Add(tools.ToolDescriptor{Name: "wait", Description: "Waits for cancellation."}, tools.ToolHandlerFunc(func(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
		select {
		case toolStarted <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return tools.ToolResponse{}, ctx.Err()
	})); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	agent := New(provider, registry)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := agent.Run(ctx, "start")
		done <- err
	}()

	select {
	case <-toolStarted:
	case <-time.After(time.Second):
		t.Fatal("tool never started")
	}

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not cancel")
	}

	if got := agent.State(); got != StateCanceled {
		t.Fatalf("agent state = %q, want %q", got, StateCanceled)
	}
}

func TestAgentCompactionTriggeredWhenEnabled(t *testing.T) {
	// First response triggers compaction (tool call to consume a turn),
	// second response is the summary, third is the final text.
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Events: []ai.Event{
				ai.ToolCallEvent{
					ContentIndex: 0,
					ToolCall: ai.ToolCall{
						ID:        "call_1",
						Name:      "echo",
						Arguments: json.RawMessage(`{"text":"from model"}`),
					},
					Complete: true,
				},
				ai.StopEvent{Reason: ai.StopReasonToolUse},
			},
		},
		providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: "summary of session"},
				ai.StopEvent{Reason: ai.StopReasonStop},
			},
		},
		providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: "done"},
				ai.StopEvent{Reason: ai.StopReasonStop},
			},
		},
	))

	registry := tools.NewRegistry()
	if err := registry.Add(tools.ToolDescriptor{
		Name:        "echo",
		Description: "Echo input text.",
	}, tools.ToolHandlerFunc(func(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
		return tools.ToolResponse{
			Content: []tools.ContentBlock{{Type: tools.ContentText, Text: "echoed"}},
		}, nil
	})); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	// Build a transcript that exceeds threshold when combined with a small context window.
	var seed []ai.Message
	for i := 0; i < 8; i++ {
		seed = append(seed, ai.Message{
			Role:    ai.RoleUser,
			Content: []ai.ContentBlock{{Type: ai.ContentText, Text: strings.Repeat("x", 400)}},
		})
	}

	agent := New(provider, registry,
		WithCompaction(true),
		WithTranscript(seed...),
		WithModel(ai.Model{ID: "test", ContextWindow: 5000}),
	)

	result, err := agent.Run(context.Background(), "trigger")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.State != StateCompleted {
		t.Fatalf("result state = %q, want %q", result.State, StateCompleted)
	}

	// Compaction should have occurred because the seeded transcript plus the
	// new user message exceeds 80% of the 5000-token context window.
	// The provider should have been called at least 2 times:
	// 1. first turn (tool call)
	// 2. summarization (during compaction)
	// Then the loop continues with the remaining response for the final turn.
	if got := provider.CallCount(); got < 2 {
		t.Fatalf("provider call count = %d, want >= 2", got)
	}
}

func TestAgentCompactionNotTriggeredWhenDisabled(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "hello"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))

	var seed []ai.Message
	for i := 0; i < 8; i++ {
		seed = append(seed, ai.Message{
			Role:    ai.RoleUser,
			Content: []ai.ContentBlock{{Type: ai.ContentText, Text: strings.Repeat("x", 400)}},
		})
	}

	agent := New(provider, tools.NewRegistry(),
		WithTranscript(seed...),
		WithModel(ai.Model{ID: "test", ContextWindow: 5000}),
	)

	_, err := agent.Run(context.Background(), "trigger")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// Compaction disabled, so only one provider call.
	if got := provider.CallCount(); got != 1 {
		t.Fatalf("provider call count = %d, want 1", got)
	}
}

type capturingProvider struct {
	*providers.FakeProvider
	mu       sync.Mutex
	requests []providers.StreamRequest
}

func (p *capturingProvider) Stream(ctx context.Context, req providers.StreamRequest) (providers.EventStream, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	return p.FakeProvider.Stream(ctx, req)
}

func (p *capturingProvider) Requests() []providers.StreamRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]providers.StreamRequest, len(p.requests))
	copy(out, p.requests)
	return out
}

func TestAgentSteer(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: "first"},
				ai.StopEvent{Reason: ai.StopReasonStop},
			},
			Delay: 50 * time.Millisecond,
		},
		providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: "second"},
				ai.StopEvent{Reason: ai.StopReasonStop},
			},
		},
	))
	registry := tools.NewRegistry()

	agent := New(provider, registry)
	go func() {
		time.Sleep(10 * time.Millisecond)
		agent.Steer(ai.Message{
			Role:    ai.RoleUser,
			Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "steered"}},
		})
	}()

	result, err := agent.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.State != StateCompleted {
		t.Fatalf("state = %q, want completed", result.State)
	}
	// Steering injects a user message mid-run, causing a second turn.
	if result.Turns != 2 {
		t.Fatalf("turns = %d, want 2 (steering causes second assistant request)", result.Turns)
	}
}

func TestAgentFollowUp(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: "first"},
				ai.StopEvent{Reason: ai.StopReasonStop},
			},
		},
	))
	registry := tools.NewRegistry()

	agent := New(provider, registry)
	agent.FollowUp(ai.Message{
		Role:    ai.RoleUser,
		Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "followup"}},
	})

	result, err := agent.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.State != StateCompleted {
		t.Fatalf("state = %q, want completed", result.State)
	}
	// FollowUp should be in the transcript after the run.
	msgs := agent.Transcript()
	var hasFollowUp bool
	for _, m := range msgs {
		if m.Role == ai.RoleUser {
			for _, c := range m.Content {
				if c.Text == "followup" {
					hasFollowUp = true
				}
			}
		}
	}
	if !hasFollowUp {
		t.Fatal("follow-up message not found in transcript")
	}
}

func TestAgentAbort(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: "hello"},
			},
			Delay: 500 * time.Millisecond,
		},
	))
	registry := tools.NewRegistry()

	agent := New(provider, registry)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		time.Sleep(50 * time.Millisecond)
		agent.Abort()
	}()

	result, err := agent.Run(ctx, "hi")
	if err == nil {
		t.Fatal("expected error after abort")
	}
	if result.State != StateCanceled {
		t.Fatalf("state = %q, want canceled", result.State)
	}
}

func TestAgentSubscribe(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "hello"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))
	registry := tools.NewRegistry()

	var events1, events2 []Event
	agent := New(provider, registry)
	agent.Subscribe(func(e Event) { events1 = append(events1, e) })
	agent.Subscribe(func(e Event) { events2 = append(events2, e) })

	_, err := agent.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(events1) == 0 {
		t.Fatal("sink 1 received no events")
	}
	if len(events2) == 0 {
		t.Fatal("sink 2 received no events")
	}
	if len(events1) != len(events2) {
		t.Fatalf("sink event counts differ: %d vs %d", len(events1), len(events2))
	}
}
