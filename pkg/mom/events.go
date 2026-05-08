package mom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// EventsBus is the subset of the dispatcher interface used by EventsWatcher to
// inject synthetic events.
type EventsBus interface {
	DispatchSyntheticEvent(event SlackEvent) bool
}

// EventsWatcher schedules and fires Slack events sourced from a directory of
// JSON files. The implementation is poll-based to stay portable across
// filesystems and to keep zero third-party dependencies.
type EventsWatcher struct {
	dir       string
	bus       EventsBus
	clock     Clock
	startedAt time.Time
	staleAt   time.Time

	mu        sync.Mutex
	known     map[string]watchedEvent
	stopped   bool
	closeChan chan struct{}
}

type watchedEvent struct {
	event    MomEvent
	cron     *CronSchedule
	location *time.Location
	nextFire time.Time
	modTime  time.Time
}

// NewEventsWatcher creates a watcher that monitors `dir` and pushes events
// onto bus.
func NewEventsWatcher(dir string, bus EventsBus, clock Clock) *EventsWatcher {
	if clock == nil {
		clock = SystemClock()
	}
	now := clock.Now()
	return &EventsWatcher{
		dir:       dir,
		bus:       bus,
		clock:     clock,
		startedAt: now,
		staleAt:   time.Now(),
		known:     make(map[string]watchedEvent),
		closeChan: make(chan struct{}),
	}
}

// SetStaleAt overrides the wall-clock threshold used to detect stale immediate
// events. Tests use this to write files with an explicitly old or new mtime.
func (w *EventsWatcher) SetStaleAt(t time.Time) {
	w.mu.Lock()
	w.staleAt = t
	w.mu.Unlock()
}

// Start eagerly scans the directory and begins the background poll loop.
func (w *EventsWatcher) Start(ctx context.Context, pollInterval time.Duration) error {
	if w.bus == nil {
		return errors.New("events watcher: bus is required")
	}
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return err
	}
	w.scan()
	go w.run(ctx, pollInterval)
	return nil
}

// Stop signals the poll loop to exit and clears tracked timers.
func (w *EventsWatcher) Stop() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.stopped = true
	close(w.closeChan)
	w.known = map[string]watchedEvent{}
	w.mu.Unlock()
}

// Tick performs a single scan/dispatch cycle. Exposed for tests so they can
// drive the watcher deterministically without sleeping.
func (w *EventsWatcher) Tick(now time.Time) {
	w.scan()
	w.fireDue(now)
}

func (w *EventsWatcher) run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.closeChan:
			return
		case <-t.C:
			w.Tick(w.clock.Now())
		}
	}
}

func (w *EventsWatcher) scan() {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	current := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		current[entry.Name()] = struct{}{}
		w.observe(entry.Name())
	}
	w.mu.Lock()
	for name := range w.known {
		if _, ok := current[name]; !ok {
			delete(w.known, name)
		}
	}
	w.mu.Unlock()
}

func (w *EventsWatcher) observe(filename string) {
	path := filepath.Join(w.dir, filename)
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	w.mu.Lock()
	prev, exists := w.known[filename]
	w.mu.Unlock()
	if exists && !info.ModTime().After(prev.modTime) {
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	event, err := decodeEvent(data, filename)
	if err != nil {
		w.deleteFile(filename)
		return
	}

	switch event.Type {
	case EventImmediate:
		w.handleImmediate(filename, event, info)
	case EventOneShot:
		w.handleOneShot(filename, event, info)
	case EventPeriodic:
		w.handlePeriodic(filename, event, info)
	default:
		w.deleteFile(filename)
	}
}

func decodeEvent(data []byte, filename string) (MomEvent, error) {
	var event MomEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return MomEvent{}, fmt.Errorf("decode %s: %w", filename, err)
	}
	if event.Type == "" || event.ChannelID == "" || strings.TrimSpace(event.Text) == "" {
		return MomEvent{}, fmt.Errorf("missing required fields in %s", filename)
	}
	switch event.Type {
	case EventImmediate:
		return event, nil
	case EventOneShot:
		if strings.TrimSpace(event.At) == "" {
			return MomEvent{}, fmt.Errorf("missing 'at' for one-shot in %s", filename)
		}
		if _, err := time.Parse(time.RFC3339, event.At); err != nil {
			return MomEvent{}, fmt.Errorf("invalid 'at' for %s: %w", filename, err)
		}
		return event, nil
	case EventPeriodic:
		if strings.TrimSpace(event.Schedule) == "" {
			return MomEvent{}, fmt.Errorf("missing 'schedule' for periodic in %s", filename)
		}
		if strings.TrimSpace(event.Timezone) == "" {
			return MomEvent{}, fmt.Errorf("missing 'timezone' for periodic in %s", filename)
		}
		return event, nil
	default:
		return MomEvent{}, fmt.Errorf("unknown event type %q in %s", event.Type, filename)
	}
}

func (w *EventsWatcher) handleImmediate(filename string, event MomEvent, info os.FileInfo) {
	w.mu.Lock()
	stale := info.ModTime().Before(w.staleAt)
	w.mu.Unlock()
	if stale {
		w.deleteFile(filename)
		return
	}
	if w.dispatch(event) {
		w.deleteFile(filename)
	}
}

func (w *EventsWatcher) handleOneShot(filename string, event MomEvent, info os.FileInfo) {
	at, err := time.Parse(time.RFC3339, event.At)
	if err != nil {
		w.deleteFile(filename)
		return
	}
	w.mu.Lock()
	w.known[filename] = watchedEvent{event: event, nextFire: at.UTC(), modTime: info.ModTime()}
	w.mu.Unlock()
}

func (w *EventsWatcher) handlePeriodic(filename string, event MomEvent, info os.FileInfo) {
	cron, err := ParseCron(event.Schedule)
	if err != nil {
		w.deleteFile(filename)
		return
	}
	loc, err := time.LoadLocation(event.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := w.clock.Now()
	next := cron.Next(now.Add(-time.Minute), loc)
	w.mu.Lock()
	w.known[filename] = watchedEvent{event: event, cron: &cron, location: loc, nextFire: next, modTime: info.ModTime()}
	w.mu.Unlock()
}

func (w *EventsWatcher) fireDue(now time.Time) {
	w.mu.Lock()
	dueNames := make([]string, 0)
	for name, watched := range w.known {
		if watched.nextFire.IsZero() || !watched.nextFire.After(now) {
			dueNames = append(dueNames, name)
		}
	}
	sort.Strings(dueNames)
	w.mu.Unlock()

	for _, name := range dueNames {
		w.mu.Lock()
		watched, ok := w.known[name]
		w.mu.Unlock()
		if !ok {
			continue
		}
		fired := w.dispatch(watched.event)
		if watched.cron == nil {
			if fired {
				w.deleteFile(name)
			}
			continue
		}
		if !fired {
			continue
		}
		next := watched.cron.Next(now.Add(time.Minute), watched.location)
		w.mu.Lock()
		watched.nextFire = next
		w.known[name] = watched
		w.mu.Unlock()
	}
}

func (w *EventsWatcher) dispatch(event MomEvent) bool {
	scheduleInfo := string(event.Type)
	switch event.Type {
	case EventOneShot:
		scheduleInfo = event.At
	case EventPeriodic:
		scheduleInfo = event.Schedule
	}
	text := fmt.Sprintf("[EVENT:%s:%s:%s] %s", "synthetic", event.Type, scheduleInfo, event.Text)
	synthetic := SlackEvent{
		Type:    EventMention,
		Channel: event.ChannelID,
		User:    "EVENT",
		Text:    text,
		TS:      SyntheticEventID(w.clock),
	}
	return w.bus.DispatchSyntheticEvent(synthetic)
}

func (w *EventsWatcher) deleteFile(filename string) {
	w.mu.Lock()
	delete(w.known, filename)
	w.mu.Unlock()
	_ = os.Remove(filepath.Join(w.dir, filename))
}
