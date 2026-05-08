package mom

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type recordingBus struct {
	mu     sync.Mutex
	events []SlackEvent
	accept bool
}

func newRecordingBus(accept bool) *recordingBus {
	return &recordingBus{accept: accept}
}

func (b *recordingBus) DispatchSyntheticEvent(event SlackEvent) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event)
	return b.accept
}

func (b *recordingBus) snapshot() []SlackEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]SlackEvent, len(b.events))
	copy(out, b.events)
	return out
}

func writeEvent(t *testing.T, dir, name string, event MomEvent) string {
	t.Helper()
	path := filepath.Join(dir, name)
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return path
}

func TestEventsWatcherImmediateFires(t *testing.T) {
	dir := t.TempDir()
	bus := newRecordingBus(true)
	clk := &FakeClock{Current: time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)}
	w := NewEventsWatcher(dir, bus, clk)
	path := writeEvent(t, dir, "alpha.json", MomEvent{Type: EventImmediate, ChannelID: "C1", Text: "go"})
	w.Tick(clk.Now())
	if got := bus.snapshot(); len(got) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(got))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected file removed, stat err = %v", err)
	}
}

func TestEventsWatcherImmediateDeletesStaleFiles(t *testing.T) {
	dir := t.TempDir()
	bus := newRecordingBus(true)
	clk := &FakeClock{Current: time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)}
	w := NewEventsWatcher(dir, bus, clk)
	// Pretend the watcher started after the file was written, so the file is stale.
	w.SetStaleAt(time.Now().Add(time.Hour))
	path := writeEvent(t, dir, "stale.json", MomEvent{Type: EventImmediate, ChannelID: "C1", Text: "old"})
	w.Tick(clk.Now())
	if got := bus.snapshot(); len(got) != 0 {
		t.Fatalf("expected stale event to be skipped, got %d dispatches", len(got))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected stale file to be removed, stat err = %v", err)
	}
}

func TestEventsWatcherOneShotFiresAtTime(t *testing.T) {
	dir := t.TempDir()
	bus := newRecordingBus(true)
	clk := &FakeClock{Current: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
	w := NewEventsWatcher(dir, bus, clk)
	at := clk.Now().Add(time.Hour).Format(time.RFC3339)
	writeEvent(t, dir, "alarm.json", MomEvent{Type: EventOneShot, ChannelID: "C1", Text: "ring", At: at})
	w.Tick(clk.Now())
	if got := bus.snapshot(); len(got) != 0 {
		t.Fatalf("expected no fire before scheduled time, got %d", len(got))
	}
	clk.Advance(2 * time.Hour)
	w.Tick(clk.Now())
	if got := bus.snapshot(); len(got) != 1 {
		t.Fatalf("expected 1 fire after scheduled time, got %d", len(got))
	}
}

func TestEventsWatcherPeriodicReschedules(t *testing.T) {
	dir := t.TempDir()
	bus := newRecordingBus(true)
	clk := &FakeClock{Current: time.Date(2026, 5, 1, 8, 30, 0, 0, time.UTC)}
	w := NewEventsWatcher(dir, bus, clk)
	writeEvent(t, dir, "periodic.json", MomEvent{
		Type: EventPeriodic, ChannelID: "C1", Text: "tick",
		Schedule: "0 9 * * *", Timezone: "UTC",
	})
	w.Tick(clk.Now())
	clk.Advance(45 * time.Minute) // now 9:15
	w.Tick(clk.Now())
	if got := bus.snapshot(); len(got) != 1 {
		t.Fatalf("expected 1 dispatch after first cron hit, got %d", len(got))
	}
	clk.Advance(24 * time.Hour) // next day
	w.Tick(clk.Now())
	if got := bus.snapshot(); len(got) != 2 {
		t.Fatalf("expected periodic to fire again, got %d", len(got))
	}
}

func TestEventsWatcherInvalidEventDeleted(t *testing.T) {
	dir := t.TempDir()
	bus := newRecordingBus(true)
	clk := &FakeClock{Current: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
	w := NewEventsWatcher(dir, bus, clk)
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	w.Tick(clk.Now())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected invalid file to be removed, err = %v", err)
	}
	if got := bus.snapshot(); len(got) != 0 {
		t.Fatalf("did not expect dispatches, got %d", len(got))
	}
}
