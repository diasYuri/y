package mom

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/tools"
)

func newTestServer(t *testing.T, dir string, connector *FakeConnector, builder func(string) (*agent.Agent, error)) *Server {
	t.Helper()
	store, err := NewChannelStore(StoreConfig{WorkingDir: dir, Clock: &FakeClock{Current: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatalf("NewChannelStore: %v", err)
	}
	server, err := NewServer(HandlerConfig{
		WorkingDir: dir,
		Connector:  connector,
		Store:      store,
		Sandbox:    &FakeSandbox{},
		BuildAgent: builder,
		Logger:     &bytes.Buffer{},
		Clock:      &FakeClock{Current: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)},
		QueueLimit: 5,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return server
}

func defaultAgentBuilder(text string) func(string) (*agent.Agent, error) {
	return func(_ string) (*agent.Agent, error) {
		provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: text},
				ai.StopEvent{Reason: ai.StopReasonStop},
			},
		}))
		return agent.New(provider, tools.NewRegistry()), nil
	}
}

func waitForUpdates(t *testing.T, connector *FakeConnector, want int, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if len(connector.Updates())+len(connector.Posts()) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d posts/updates; posts=%d updates=%d", want, len(connector.Posts()), len(connector.Updates()))
}

func TestServerDispatchUserEventRunsAgent(t *testing.T) {
	dir := t.TempDir()
	connector := NewFakeConnector("B1", []SlackUser{{ID: "U1", UserName: "alice"}}, []SlackChannel{{ID: "C1", Name: "general"}})
	server := newTestServer(t, dir, connector, defaultAgentBuilder("hi from agent"))
	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer server.Stop()

	connector.PushEvent(SlackEvent{Type: EventMention, Channel: "C1", User: "U1", Text: "hello mom", TS: "1.000000"})
	waitForUpdates(t, connector, 2, 2*time.Second)
	combined := connector.Posts()
	updates := connector.Updates()
	found := false
	for _, p := range combined {
		if strings.Contains(p.Text, "hi from agent") {
			found = true
		}
	}
	for _, u := range updates {
		if strings.Contains(u.Text, "hi from agent") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected final agent text in posts/updates; posts=%#v updates=%#v", combined, updates)
	}
}

func TestServerSyntheticEventTriggersAgent(t *testing.T) {
	dir := t.TempDir()
	connector := NewFakeConnector("B1", nil, []SlackChannel{{ID: "C1", Name: "general"}})
	server := newTestServer(t, dir, connector, defaultAgentBuilder("synthetic answer"))
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer server.Stop()

	if !connector.PushSynthetic(SlackEvent{Type: EventMention, Channel: "C1", User: "EVENT", Text: "[EVENT:test:immediate:immediate] tick", TS: "2.000000"}) {
		t.Fatal("PushSynthetic returned false")
	}
	waitForUpdates(t, connector, 2, 2*time.Second)
}

func TestServerStopAbortsRun(t *testing.T) {
	dir := t.TempDir()
	connector := NewFakeConnector("B1", []SlackUser{{ID: "U1", UserName: "alice"}}, []SlackChannel{{ID: "C1", Name: "general"}})

	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "slow"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
		Delay: 200 * time.Millisecond,
	}))
	builder := func(_ string) (*agent.Agent, error) {
		return agent.New(provider, tools.NewRegistry()), nil
	}
	server := newTestServer(t, dir, connector, builder)
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer server.Stop()

	connector.PushEvent(SlackEvent{Type: EventMention, Channel: "C1", User: "U1", Text: "go", TS: "1.000000"})
	time.Sleep(20 * time.Millisecond)
	connector.PushEvent(SlackEvent{Type: EventMention, Channel: "C1", User: "U1", Text: "stop", TS: "2.000000"})
	waitForUpdates(t, connector, 1, 2*time.Second)

	// Look for "_Stopping..._" or "_Stopped_" in posts
	posts := connector.Posts()
	stopFound := false
	for _, p := range posts {
		if strings.Contains(p.Text, "_Stopping..._") || strings.Contains(p.Text, "_Stopped_") {
			stopFound = true
			break
		}
	}
	if !stopFound {
		t.Fatalf("expected stop acknowledgement, got posts=%#v", posts)
	}
}

func TestServerLogsUserMessage(t *testing.T) {
	dir := t.TempDir()
	connector := NewFakeConnector("B1", []SlackUser{{ID: "U1", UserName: "alice", DisplayName: "Alice"}}, []SlackChannel{{ID: "C1", Name: "general"}})
	server := newTestServer(t, dir, connector, defaultAgentBuilder("ok"))
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer server.Stop()
	connector.PushEvent(SlackEvent{Type: EventMention, Channel: "C1", User: "U1", Text: "hi", TS: "10.0"})
	waitForUpdates(t, connector, 1, 2*time.Second)
}
