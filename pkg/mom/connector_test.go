package mom

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

type stubDispatcher struct {
	user   atomic.Int64
	syn    atomic.Int64
	mu     sync.Mutex
	events []SlackEvent
}

func (s *stubDispatcher) DispatchUserEvent(event SlackEvent) {
	s.user.Add(1)
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
}

func (s *stubDispatcher) DispatchSyntheticEvent(event SlackEvent) bool {
	s.syn.Add(1)
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	return true
}

func TestFakeConnectorDispatchesAndPosts(t *testing.T) {
	users := []SlackUser{{ID: "U1", UserName: "alice"}}
	channels := []SlackChannel{{ID: "C1", Name: "general"}}
	c := NewFakeConnector("B1", users, channels)
	d := &stubDispatcher{}
	if err := c.Start(context.Background(), d); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if c.BotUserID() != "B1" {
		t.Fatalf("BotUserID mismatch")
	}
	if u, ok := c.GetUser("U1"); !ok || u.UserName != "alice" {
		t.Fatalf("GetUser: %v %v", ok, u)
	}
	if ch, ok := c.GetChannel("C1"); !ok || ch.Name != "general" {
		t.Fatalf("GetChannel: %v %v", ok, ch)
	}
	if len(c.AllUsers()) != 1 || len(c.AllChannels()) != 1 {
		t.Fatalf("expected list sizes 1/1, got %d/%d", len(c.AllUsers()), len(c.AllChannels()))
	}
	ts, err := c.PostMessage(context.Background(), "C1", "hi")
	if err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if ts == "" {
		t.Fatal("ts is empty")
	}
	if err := c.UpdateMessage(context.Background(), "C1", ts, "edited"); err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}
	if err := c.DeleteMessage(context.Background(), "C1", ts); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if _, err := c.PostInThread(context.Background(), "C1", ts, "thread"); err != nil {
		t.Fatalf("PostInThread: %v", err)
	}
	if err := c.UploadFile(context.Background(), "C1", "/tmp/file.txt", "title"); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	c.PushEvent(SlackEvent{Type: EventMention, Channel: "C1", User: "U1", Text: "hi"})
	c.PushSynthetic(SlackEvent{Type: EventMention, Channel: "C1", User: "EVENT", Text: "tick"})
	if d.user.Load() != 1 || d.syn.Load() != 1 {
		t.Fatalf("dispatch counts user=%d syn=%d", d.user.Load(), d.syn.Load())
	}

	if len(c.Posts()) != 1 || len(c.Updates()) != 1 || len(c.Deletes()) != 1 || len(c.Threads()) != 1 || len(c.Uploads()) != 1 {
		t.Fatalf("recorded counts unexpected")
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if c.PushEvent(SlackEvent{}) {
		t.Fatal("PushEvent should return false after Stop")
	}
}

func TestSyntheticEventID(t *testing.T) {
	got := SyntheticEventID(&FakeClock{})
	if got == "" {
		t.Fatal("expected non-empty synthetic id")
	}
}
