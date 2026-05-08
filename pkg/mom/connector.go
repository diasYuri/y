package mom

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
)

// Connector abstracts the Slack workspace transport. Production code wires
// this to a Socket Mode + Web API client; tests use FakeConnector.
type Connector interface {
	Start(ctx context.Context, dispatcher EventDispatcher) error
	Stop() error
	BotUserID() string
	GetUser(userID string) (SlackUser, bool)
	GetChannel(channelID string) (SlackChannel, bool)
	AllUsers() []SlackUser
	AllChannels() []SlackChannel
	PostMessage(ctx context.Context, channel, text string) (string, error)
	UpdateMessage(ctx context.Context, channel, ts, text string) error
	DeleteMessage(ctx context.Context, channel, ts string) error
	PostInThread(ctx context.Context, channel, threadTS, text string) (string, error)
	UploadFile(ctx context.Context, channel, filePath, title string) error
}

// EventDispatcher receives events emitted by the connector. The Server
// implements this interface; tests can implement it as well.
type EventDispatcher interface {
	DispatchUserEvent(event SlackEvent)
	DispatchSyntheticEvent(event SlackEvent) bool
}

// FakeConnector is an in-memory connector used by tests and the default
// `cmd/y-mom` build. It records posts, updates, deletions and threads, and
// allows tests to drive inbound events through PushEvent/PushSynthetic.
type FakeConnector struct {
	mu          sync.Mutex
	botID       string
	users       map[string]SlackUser
	channels    map[string]SlackChannel
	dispatcher  EventDispatcher
	posts       []FakePost
	updates     []FakeUpdate
	deletes     []FakeDelete
	threads     []FakePost
	uploads     []FakeUpload
	postCounter int64
	stopped     bool
}

// FakePost captures a posted (or threaded) Slack message.
type FakePost struct {
	Channel  string
	TS       string
	ThreadTS string
	Text     string
}

// FakeUpdate captures a chat.update call.
type FakeUpdate struct {
	Channel string
	TS      string
	Text    string
}

// FakeDelete captures a chat.delete call.
type FakeDelete struct {
	Channel string
	TS      string
}

// FakeUpload captures a files.upload call.
type FakeUpload struct {
	Channel string
	Path    string
	Title   string
}

// NewFakeConnector constructs a FakeConnector with the supplied bot id, users
// and channels.
func NewFakeConnector(botID string, users []SlackUser, channels []SlackChannel) *FakeConnector {
	c := &FakeConnector{
		botID:    botID,
		users:    make(map[string]SlackUser, len(users)),
		channels: make(map[string]SlackChannel, len(channels)),
	}
	for _, u := range users {
		c.users[u.ID] = u
	}
	for _, ch := range channels {
		c.channels[ch.ID] = ch
	}
	return c
}

// Start records the dispatcher and marks the connector as live.
func (c *FakeConnector) Start(_ context.Context, dispatcher EventDispatcher) error {
	if dispatcher == nil {
		return errors.New("fake connector: dispatcher is required")
	}
	c.mu.Lock()
	c.dispatcher = dispatcher
	c.stopped = false
	c.mu.Unlock()
	return nil
}

// Stop disables further dispatch.
func (c *FakeConnector) Stop() error {
	c.mu.Lock()
	c.stopped = true
	c.mu.Unlock()
	return nil
}

// BotUserID returns the configured bot user ID.
func (c *FakeConnector) BotUserID() string { return c.botID }

// GetUser reports the user known under id.
func (c *FakeConnector) GetUser(id string) (SlackUser, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	u, ok := c.users[id]
	return u, ok
}

// GetChannel reports the channel known under id.
func (c *FakeConnector) GetChannel(id string) (SlackChannel, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.channels[id]
	return ch, ok
}

// AllUsers returns a stable copy of every known user.
func (c *FakeConnector) AllUsers() []SlackUser {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]SlackUser, 0, len(c.users))
	for _, u := range c.users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// AllChannels returns a stable copy of every known channel.
func (c *FakeConnector) AllChannels() []SlackChannel {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]SlackChannel, 0, len(c.channels))
	for _, ch := range c.channels {
		out = append(out, ch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// PostMessage records a chat.postMessage and assigns a synthetic ts.
func (c *FakeConnector) PostMessage(_ context.Context, channel, text string) (string, error) {
	c.mu.Lock()
	c.postCounter++
	ts := "p" + strconv.FormatInt(c.postCounter, 10)
	c.posts = append(c.posts, FakePost{Channel: channel, TS: ts, Text: text})
	c.mu.Unlock()
	return ts, nil
}

// UpdateMessage records a chat.update.
func (c *FakeConnector) UpdateMessage(_ context.Context, channel, ts, text string) error {
	c.mu.Lock()
	c.updates = append(c.updates, FakeUpdate{Channel: channel, TS: ts, Text: text})
	c.mu.Unlock()
	return nil
}

// DeleteMessage records a chat.delete.
func (c *FakeConnector) DeleteMessage(_ context.Context, channel, ts string) error {
	c.mu.Lock()
	c.deletes = append(c.deletes, FakeDelete{Channel: channel, TS: ts})
	c.mu.Unlock()
	return nil
}

// PostInThread records a thread reply.
func (c *FakeConnector) PostInThread(_ context.Context, channel, threadTS, text string) (string, error) {
	c.mu.Lock()
	c.postCounter++
	ts := "t" + strconv.FormatInt(c.postCounter, 10)
	c.threads = append(c.threads, FakePost{Channel: channel, TS: ts, ThreadTS: threadTS, Text: text})
	c.mu.Unlock()
	return ts, nil
}

// UploadFile records a files.upload call.
func (c *FakeConnector) UploadFile(_ context.Context, channel, path, title string) error {
	c.mu.Lock()
	c.uploads = append(c.uploads, FakeUpload{Channel: channel, Path: path, Title: title})
	c.mu.Unlock()
	return nil
}

// PushEvent dispatches a user-originated event through the registered
// dispatcher. It returns false when the connector has been stopped.
func (c *FakeConnector) PushEvent(event SlackEvent) bool {
	c.mu.Lock()
	dispatcher := c.dispatcher
	stopped := c.stopped
	c.mu.Unlock()
	if dispatcher == nil || stopped {
		return false
	}
	dispatcher.DispatchUserEvent(event)
	return true
}

// PushSynthetic dispatches a synthetic event (e.g. from EventsWatcher).
func (c *FakeConnector) PushSynthetic(event SlackEvent) bool {
	c.mu.Lock()
	dispatcher := c.dispatcher
	stopped := c.stopped
	c.mu.Unlock()
	if dispatcher == nil || stopped {
		return false
	}
	return dispatcher.DispatchSyntheticEvent(event)
}

// Posts returns a copy of recorded direct posts.
func (c *FakeConnector) Posts() []FakePost { return cloneSlice(c.posts) }

// Updates returns a copy of recorded chat.update calls.
func (c *FakeConnector) Updates() []FakeUpdate { return cloneSlice(c.updates) }

// Deletes returns a copy of recorded chat.delete calls.
func (c *FakeConnector) Deletes() []FakeDelete { return cloneSlice(c.deletes) }

// Threads returns a copy of recorded threaded replies.
func (c *FakeConnector) Threads() []FakePost { return cloneSlice(c.threads) }

// Uploads returns a copy of recorded file uploads.
func (c *FakeConnector) Uploads() []FakeUpload { return cloneSlice(c.uploads) }

func cloneSlice[T any](in []T) []T {
	out := make([]T, len(in))
	copy(out, in)
	return out
}

// SyntheticEventID generates a synthetic ts string from the supplied clock.
func SyntheticEventID(clock Clock) string {
	if clock == nil {
		clock = SystemClock()
	}
	return strconv.FormatInt(clock.Now().UnixMilli(), 10) + ".000000"
}

// LooksLikeStop returns true when the supplied text is the literal "stop"
// command (case-insensitive, trimmed).
func LooksLikeStop(text string) bool {
	t := text
	for len(t) > 0 && (t[0] == ' ' || t[0] == '\t' || t[0] == '\n') {
		t = t[1:]
	}
	for len(t) > 0 && (t[len(t)-1] == ' ' || t[len(t)-1] == '\t' || t[len(t)-1] == '\n') {
		t = t[:len(t)-1]
	}
	if len(t) != 4 {
		return false
	}
	return (t[0] == 'S' || t[0] == 's') &&
		(t[1] == 'T' || t[1] == 't') &&
		(t[2] == 'O' || t[2] == 'o') &&
		(t[3] == 'P' || t[3] == 'p')
}
