package mom

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/tools"
)

func newFakeAgent(t *testing.T, text string) *agent.Agent {
	t.Helper()
	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: text},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))
	registry := tools.NewRegistry()
	return agent.New(provider, registry)
}

func TestSlackContextPostsThenUpdates(t *testing.T) {
	connector := NewFakeConnector("B1", []SlackUser{{ID: "U1", UserName: "u"}}, []SlackChannel{{ID: "C1", Name: "general"}})
	ctx := context.Background()
	sc, err := NewSlackContext(SlackContextOptions{Channel: "C1", UserID: "U1", Connector: connector})
	if err != nil {
		t.Fatalf("NewSlackContext: %v", err)
	}
	if err := sc.Respond(ctx, "hello", false); err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if got := connector.Posts(); len(got) != 1 {
		t.Fatalf("expected 1 post, got %d", len(got))
	}
	if err := sc.Respond(ctx, "world", false); err != nil {
		t.Fatalf("Respond second: %v", err)
	}
	updates := connector.Updates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	if !strings.Contains(updates[0].Text, "hello") || !strings.Contains(updates[0].Text, "world") {
		t.Fatalf("update text = %q", updates[0].Text)
	}
}

func TestSlackContextRespondInThread(t *testing.T) {
	connector := NewFakeConnector("B1", nil, nil)
	sc, err := NewSlackContext(SlackContextOptions{Channel: "C1", Connector: connector})
	if err != nil {
		t.Fatalf("NewSlackContext: %v", err)
	}
	if err := sc.RespondInThread(context.Background(), "thread"); err == nil {
		t.Fatal("expected error when no main message yet")
	}
	if err := sc.Respond(context.Background(), "main", false); err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if err := sc.RespondInThread(context.Background(), "thread reply"); err != nil {
		t.Fatalf("RespondInThread: %v", err)
	}
	threads := connector.Threads()
	if len(threads) != 1 || threads[0].Text != "thread reply" {
		t.Fatalf("unexpected threads = %#v", threads)
	}
}

func TestAgentRunnerRunCallsAgent(t *testing.T) {
	connector := NewFakeConnector("B1", nil, nil)
	dir := t.TempDir()
	store, err := NewChannelStore(StoreConfig{WorkingDir: dir, Clock: &FakeClock{Current: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatalf("NewChannelStore: %v", err)
	}
	sc, err := NewSlackContext(SlackContextOptions{Channel: "C1", Connector: connector, Store: store})
	if err != nil {
		t.Fatalf("NewSlackContext: %v", err)
	}
	runner, err := NewAgentRunner(AgentRunnerOptions{Agent: newFakeAgent(t, "hello there")})
	if err != nil {
		t.Fatalf("NewAgentRunner: %v", err)
	}
	res, err := runner.Run(context.Background(), sc, "hi mom")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != string(ai.StopReasonStop) {
		t.Fatalf("StopReason = %q", res.StopReason)
	}
	if got := connector.Posts(); len(got) == 0 {
		t.Fatalf("expected at least one post, got 0")
	}
	last := connector.Updates()
	found := false
	for _, u := range last {
		if strings.Contains(u.Text, "hello there") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("final assistant text not found; updates = %#v posts = %#v", last, connector.Posts())
	}
}

func TestAgentRunnerAbortPropagates(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "slow"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
		Delay: 50 * time.Millisecond,
	}))
	connector := NewFakeConnector("B1", nil, nil)
	sc, err := NewSlackContext(SlackContextOptions{Channel: "C1", Connector: connector})
	if err != nil {
		t.Fatalf("NewSlackContext: %v", err)
	}
	runner, err := NewAgentRunner(AgentRunnerOptions{Agent: agent.New(provider, tools.NewRegistry())})
	if err != nil {
		t.Fatalf("NewAgentRunner: %v", err)
	}
	go func() {
		time.Sleep(5 * time.Millisecond)
		runner.Abort()
	}()
	res, err := runner.Run(context.Background(), sc, "go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != "aborted" {
		t.Fatalf("StopReason = %q, want aborted", res.StopReason)
	}
}
