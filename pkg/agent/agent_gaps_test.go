package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/tools"
)

func TestSubscribeUnsubscribe(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "hi"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))
	registry := tools.NewRegistry()

	a := New(provider, registry)
	var keepCount, dropCount int
	keep := a.Subscribe(func(Event) { keepCount++ })
	drop := a.Subscribe(func(Event) { dropCount++ })
	if keep == nil || drop == nil {
		t.Fatal("Subscribe returned nil unsubscribe func")
	}

	// Unsubscribe drop before running.
	drop()
	drop() // safe to call twice

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if keepCount == 0 {
		t.Fatal("kept sink received zero events")
	}
	if dropCount != 0 {
		t.Fatalf("dropped sink received %d events, want 0", dropCount)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "hello"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))
	registry := tools.NewRegistry()

	a := New(provider, registry,
		WithSystemPrompt("be helpful"),
		WithSessionID("sess-1"),
		WithThinkingBudgets(map[ai.ThinkingLevel]int64{ai.ThinkingLow: 100}),
		WithMaxTurns(8),
	)
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	a.FollowUp(ai.Message{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "later"}}})

	snap := a.Snapshot()
	if len(snap.Transcript) == 0 {
		t.Fatal("snapshot transcript empty")
	}
	if snap.SystemPrompt != "be helpful" {
		t.Fatalf("snapshot system prompt = %q", snap.SystemPrompt)
	}
	if snap.SessionID != "sess-1" {
		t.Fatalf("snapshot sessionID = %q", snap.SessionID)
	}
	if snap.MaxTurns != 8 {
		t.Fatalf("snapshot maxTurns = %d", snap.MaxTurns)
	}
	if got := snap.ThinkingBudgets[ai.ThinkingLow]; got != 100 {
		t.Fatalf("snapshot thinking budget = %d", got)
	}
	if len(snap.FollowUpQueue) != 1 {
		t.Fatalf("snapshot followup len = %d", len(snap.FollowUpQueue))
	}

	// Round-trip via JSON to confirm the snapshot is encodable.
	encoded, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	var decoded AgentSnapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	provider2 := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "ack"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))
	a2, err := RestoreAgent(provider2, registry, decoded)
	if err != nil {
		t.Fatalf("RestoreAgent error: %v", err)
	}
	got := a2.Transcript()
	if len(got) != len(snap.Transcript) {
		t.Fatalf("restored transcript len = %d, want %d", len(got), len(snap.Transcript))
	}
	if a2.State() != StateCompleted {
		t.Fatalf("restored state = %q, want completed", a2.State())
	}
}

func TestBeforeRequestShortCircuits(t *testing.T) {
	// The provider should never be called if BeforeRequest returns a hooked
	// response.
	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "should not be used"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))

	called := 0
	hook := BeforeRequestHook(func(ctx context.Context, req *providers.StreamRequest) (*HookedResponse, error) {
		called++
		return &HookedResponse{
			Message: ai.Message{
				Role:    ai.RoleAssistant,
				Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "cached"}},
			},
			Usage:      ai.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
			StopReason: ai.StopReasonStop,
		}, nil
	})

	a := New(provider, tools.NewRegistry(), WithBeforeRequest(hook))
	res, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if called != 1 {
		t.Fatalf("BeforeRequest called %d times, want 1", called)
	}
	if provider.CallCount() != 0 {
		t.Fatalf("provider call count = %d, want 0", provider.CallCount())
	}
	if res.Messages[len(res.Messages)-1].Content[0].Text != "cached" {
		t.Fatalf("transcript text = %q, want 'cached'", res.Messages[len(res.Messages)-1].Content[0].Text)
	}
}

func TestAfterRequestObservesUsage(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "ok"},
			ai.UsageEvent{Usage: ai.Usage{InputTokens: 7, OutputTokens: 11, TotalTokens: 18}},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))

	var seen ai.Usage
	hook := AfterRequestHook(func(ctx context.Context, req providers.StreamRequest, msg ai.Message, usage ai.Usage, err error) error {
		seen = usage
		return nil
	})
	a := New(provider, tools.NewRegistry(), WithAfterRequest(hook))
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if seen.OutputTokens != 11 || seen.InputTokens != 7 {
		t.Fatalf("AfterRequest usage = %+v, want input=7 output=11", seen)
	}
}

func TestOnErrorClassifiesRetry(t *testing.T) {
	// First response returns an error, second succeeds.
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Events: []ai.Event{
				ai.ErrorEvent{Err: errors.New("transient"), Code: "transient"},
			},
		},
		providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: "recovered"},
				ai.StopEvent{Reason: ai.StopReasonStop},
			},
		},
	))

	hookCalls := 0
	a := New(provider, tools.NewRegistry(),
		WithMaxRetries(1),
		WithOnError(func(ctx context.Context, phase ErrorPhase, err error) error {
			hookCalls++
			return ErrRetry
		}),
	)
	res, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res.State != StateCompleted {
		t.Fatalf("state = %q, want completed", res.State)
	}
	if hookCalls == 0 {
		t.Fatal("OnError hook never invoked")
	}
	if provider.CallCount() != 2 {
		t.Fatalf("provider call count = %d, want 2", provider.CallCount())
	}
}

func TestRecoverFromTransientFailure(t *testing.T) {
	// First call fails with EOF (transient), second succeeds. With
	// MaxRetries=0 the first run fails. Recover should re-execute with the
	// next response.
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Events: []ai.Event{
				ai.ErrorEvent{Err: errors.New("io: EOF: temporary"), Code: "transient"},
			},
		},
		providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: "second time"},
				ai.StopEvent{Reason: ai.StopReasonStop},
			},
		},
	))
	classify := func(ctx context.Context, phase ErrorPhase, err error) error {
		// Wrap the error so isRecoverable returns true via ErrRetry path.
		return errors.Join(err, ErrRetry)
	}
	a := New(provider, tools.NewRegistry(), WithOnError(classify))
	_, err := a.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected first run to fail")
	}
	if a.State() != StateFailed {
		t.Fatalf("state after fail = %q", a.State())
	}

	// Now Recover should pick up where it left off.
	res, err := a.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover error: %v", err)
	}
	if res.State != StateCompleted {
		t.Fatalf("recovered state = %q", res.State)
	}
}

func TestToolConcurrencyLimit(t *testing.T) {
	// Provider emits 4 tool calls; concurrency limit = 2; verify max in-flight
	// never exceeds 2.
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Events: []ai.Event{
				ai.ToolCallEvent{ContentIndex: 0, ToolCall: ai.ToolCall{ID: "1", Name: "slow", Arguments: json.RawMessage(`{}`)}, Complete: true},
				ai.ToolCallEvent{ContentIndex: 1, ToolCall: ai.ToolCall{ID: "2", Name: "slow", Arguments: json.RawMessage(`{}`)}, Complete: true},
				ai.ToolCallEvent{ContentIndex: 2, ToolCall: ai.ToolCall{ID: "3", Name: "slow", Arguments: json.RawMessage(`{}`)}, Complete: true},
				ai.ToolCallEvent{ContentIndex: 3, ToolCall: ai.ToolCall{ID: "4", Name: "slow", Arguments: json.RawMessage(`{}`)}, Complete: true},
				ai.StopEvent{Reason: ai.StopReasonToolUse},
			},
		},
		providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: "done"},
				ai.StopEvent{Reason: ai.StopReasonStop},
			},
		},
	))
	var inFlight int32
	var maxInFlight int32
	registry := tools.NewRegistry()
	if err := registry.Add(tools.ToolDescriptor{Name: "slow", Description: "slow"},
		tools.ToolHandlerFunc(func(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
			cur := atomic.AddInt32(&inFlight, 1)
			defer atomic.AddInt32(&inFlight, -1)
			for {
				old := atomic.LoadInt32(&maxInFlight)
				if cur <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			return tools.ToolResponse{Content: []tools.ContentBlock{{Type: tools.ContentText, Text: "ok"}}}, nil
		})); err != nil {
		t.Fatalf("Add: %v", err)
	}

	a := New(provider, registry, WithToolConcurrency(2))
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got := atomic.LoadInt32(&maxInFlight); got > 2 {
		t.Fatalf("max in-flight = %d, want <= 2", got)
	}
	if got := atomic.LoadInt32(&maxInFlight); got == 0 {
		t.Fatal("no tool ever ran")
	}
}

func TestToolTimeoutReturnsError(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Events: []ai.Event{
				ai.ToolCallEvent{ContentIndex: 0, ToolCall: ai.ToolCall{ID: "1", Name: "block", Arguments: json.RawMessage(`{}`)}, Complete: true},
				ai.StopEvent{Reason: ai.StopReasonToolUse},
			},
		},
		providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: "after"},
				ai.StopEvent{Reason: ai.StopReasonStop},
			},
		},
	))
	registry := tools.NewRegistry()
	if err := registry.Add(tools.ToolDescriptor{Name: "block", Description: "blocks"},
		tools.ToolHandlerFunc(func(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
			<-ctx.Done()
			return tools.ToolResponse{}, ctx.Err()
		})); err != nil {
		t.Fatalf("Add: %v", err)
	}

	a := New(provider, registry, WithToolTimeout(20*time.Millisecond))
	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	// Tool should have been recorded with an error result instead of bringing
	// down the run.
	var sawErr bool
	for _, m := range res.Messages {
		if m.Role == ai.RoleToolResult && m.ToolResult != nil && m.ToolResult.IsError {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("expected at least one tool-result error from timeout")
	}
}

func TestStreamOptionsMergePrecedence(t *testing.T) {
	temp1 := 0.1
	temp2 := 0.9
	defaults := providers.StreamOptions{Temperature: &temp1, MaxTokens: 100, SessionID: "default"}
	perCall := providers.StreamOptions{Temperature: &temp2, MaxTokens: 200}
	builtin := providers.StreamOptions{SessionID: "builtin"}

	merged := mergeStreamOptions(builtin, defaults, perCall)
	if merged.Temperature == nil || *merged.Temperature != 0.9 {
		t.Fatalf("temperature = %v, want 0.9", merged.Temperature)
	}
	if merged.MaxTokens != 200 {
		t.Fatalf("maxTokens = %d, want 200", merged.MaxTokens)
	}
	// SessionID came from defaults (per-call did not override it).
	if merged.SessionID != "default" {
		t.Fatalf("sessionID = %q, want 'default'", merged.SessionID)
	}
}

func TestRunWithOptionsPropagates(t *testing.T) {
	prov := &capturingProvider{
		FakeProvider: providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: "ok"},
				ai.StopEvent{Reason: ai.StopReasonStop},
			},
		})),
	}
	temp := 0.42
	a := New(prov, tools.NewRegistry())
	if _, err := a.RunWithOptions(context.Background(), "hi", RunOptions{
		Stream: providers.StreamOptions{Temperature: &temp, MaxTokens: 1234},
	}); err != nil {
		t.Fatalf("RunWithOptions error: %v", err)
	}
	reqs := prov.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	if reqs[0].Options.Temperature == nil || *reqs[0].Options.Temperature != 0.42 {
		t.Fatalf("temperature in request = %v, want 0.42", reqs[0].Options.Temperature)
	}
	if reqs[0].Options.MaxTokens != 1234 {
		t.Fatalf("maxTokens in request = %d, want 1234", reqs[0].Options.MaxTokens)
	}
}

func TestUsageObserverFiresEstimateOrigin(t *testing.T) {
	// Provider does not emit a UsageEvent so the agent should fall back to
	// estimation and tag UsageEstimated.
	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "estimating"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))
	var origins []UsageOrigin
	var mu sync.Mutex
	a := New(provider, tools.NewRegistry(),
		WithUsageObserver(func(o UsageOrigin, _ ai.Usage) {
			mu.Lock()
			origins = append(origins, o)
			mu.Unlock()
		}),
		WithLogger(LoggerFunc(func(string, ...interface{}) {})),
	)
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(origins) == 0 {
		t.Fatal("UsageObserver never fired")
	}
	for _, o := range origins {
		if o != UsageEstimated {
			t.Fatalf("origin = %q, want estimated", o)
		}
	}
}

func TestUsageObserverFiresReportedOrigin(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "reported"},
			ai.UsageEvent{Usage: ai.Usage{InputTokens: 5, OutputTokens: 7, TotalTokens: 12}},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))
	var origin UsageOrigin
	a := New(provider, tools.NewRegistry(), WithUsageObserver(func(o UsageOrigin, _ ai.Usage) { origin = o }))
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if origin != UsageReported {
		t.Fatalf("origin = %q, want reported", origin)
	}
}

func TestRestoreAgentValidation(t *testing.T) {
	a := New(nil, tools.NewRegistry())
	err := a.Restore(AgentSnapshot{MaxTurns: -1})
	if err == nil {
		t.Fatal("expected error for negative MaxTurns")
	}
	if !strings.Contains(err.Error(), "MaxTurns") {
		t.Fatalf("error = %v, want to mention MaxTurns", err)
	}
}
