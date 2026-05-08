package mom

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/yuri/y/pkg/agent"
)

// HandlerConfig configures the handler used by Server.
type HandlerConfig struct {
	WorkingDir   string
	SandboxCfg   SandboxConfig
	Connector    Connector
	Store        *ChannelStore
	Sandbox      Sandbox
	Logger       io.Writer
	Clock        Clock
	BuildAgent   func(channelID string) (*agent.Agent, error)
	Watcher      *EventsWatcher
	QueueLimit   int
	IdleResponse string
	StopResponse string
}

// Server orchestrates Slack events for y-mom. It is the Go equivalent of the
// TypeScript main.ts handler glue: per-channel state, per-channel queues,
// stop semantics, and synthetic event dispatch.
type Server struct {
	cfg HandlerConfig

	mu        sync.Mutex
	channels  map[string]*channelState
	queues    map[string]*channelQueue
	startedAt time.Time
	closed    bool
}

type channelState struct {
	runner      *AgentRunner
	store       *ChannelStore
	running     bool
	stopRequest bool
	mu          sync.Mutex
}

// NewServer constructs a Server from cfg.
func NewServer(cfg HandlerConfig) (*Server, error) {
	if cfg.Connector == nil {
		return nil, errors.New("server: connector is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("server: store is required")
	}
	if cfg.BuildAgent == nil {
		return nil, errors.New("server: BuildAgent is required")
	}
	if cfg.Sandbox == nil {
		return nil, errors.New("server: sandbox is required")
	}
	if cfg.Clock == nil {
		cfg.Clock = SystemClock()
	}
	if cfg.QueueLimit <= 0 {
		cfg.QueueLimit = 5
	}
	if cfg.IdleResponse == "" {
		cfg.IdleResponse = "_Already working. Say `@mom stop` to cancel._"
	}
	if cfg.StopResponse == "" {
		cfg.StopResponse = "_Nothing running_"
	}
	return &Server{
		cfg:       cfg,
		channels:  make(map[string]*channelState),
		queues:    make(map[string]*channelQueue),
		startedAt: cfg.Clock.Now(),
	}, nil
}

// Start hands the server to the connector and, if configured, kicks off the
// events watcher.
func (s *Server) Start(ctx context.Context) error {
	if err := s.cfg.Connector.Start(ctx, s); err != nil {
		return err
	}
	if s.cfg.Watcher != nil {
		if err := s.cfg.Watcher.Start(ctx, 250*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

// Stop tears the server down.
func (s *Server) Stop() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	if s.cfg.Watcher != nil {
		s.cfg.Watcher.Stop()
	}
	return s.cfg.Connector.Stop()
}

// DispatchUserEvent is called by the connector for user-originated events.
func (s *Server) DispatchUserEvent(event SlackEvent) {
	s.handleInbound(event, false)
}

// DispatchSyntheticEvent is called by the events watcher for scheduled events.
func (s *Server) DispatchSyntheticEvent(event SlackEvent) bool {
	return s.handleInbound(event, true)
}

func (s *Server) handleInbound(event SlackEvent, isSynthetic bool) bool {
	if s.cfg.Store == nil {
		return false
	}
	if event.User == "" {
		event.User = "EVENT"
	}
	attachments := s.cfg.Store.ProcessAttachments(event.Channel, event.Files, event.TS)
	event.Attachments = attachments
	user, _ := s.cfg.Connector.GetUser(event.User)
	if !isSynthetic {
		_, err := s.cfg.Store.LogMessage(event.Channel, LoggedMessage{
			TS:          event.TS,
			User:        event.User,
			UserName:    user.UserName,
			DisplayName: user.DisplayName,
			Text:        event.Text,
			Attachments: attachments,
			IsBot:       false,
		})
		if err != nil {
			s.warn("log error", err)
		}
	}

	stop := LooksLikeStop(event.Text)
	state := s.getState(event.Channel)
	if stop && !isSynthetic {
		s.handleStop(event, state)
		return true
	}

	queue := s.getQueue(event.Channel)
	if queue.size() >= s.cfg.QueueLimit {
		s.warn("queue full", fmt.Errorf("dropping event for channel %s", event.Channel))
		return false
	}
	queue.enqueue(func() { s.runHandler(event, state) })
	return true
}

func (s *Server) handleStop(event SlackEvent, state *channelState) {
	state.mu.Lock()
	running := state.running
	state.mu.Unlock()

	ctx := context.Background()
	if !running {
		_, _ = s.cfg.Connector.PostMessage(ctx, event.Channel, s.cfg.StopResponse)
		return
	}
	state.mu.Lock()
	state.stopRequest = true
	if state.runner != nil {
		state.runner.Abort()
	}
	state.mu.Unlock()
	_, _ = s.cfg.Connector.PostMessage(ctx, event.Channel, "_Stopping..._")
}

func (s *Server) runHandler(event SlackEvent, state *channelState) {
	state.mu.Lock()
	if state.running {
		state.mu.Unlock()
		_, _ = s.cfg.Connector.PostMessage(context.Background(), event.Channel, s.cfg.IdleResponse)
		return
	}
	state.running = true
	state.stopRequest = false
	state.mu.Unlock()

	defer func() {
		state.mu.Lock()
		state.running = false
		state.mu.Unlock()
	}()

	channelInfo, _ := s.cfg.Connector.GetChannel(event.Channel)
	user, _ := s.cfg.Connector.GetUser(event.User)
	sc, err := NewSlackContext(SlackContextOptions{
		Channel:     event.Channel,
		ChannelName: channelInfo.Name,
		UserID:      event.User,
		UserName:    user.UserName,
		Connector:   s.cfg.Connector,
		Store:       s.cfg.Store,
		Logger:      s.cfg.Logger,
	})
	if err != nil {
		s.warn("slack ctx error", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ag, err := s.cfg.BuildAgent(event.Channel)
	if err != nil {
		s.warn("build agent error", err)
		return
	}
	runner, err := NewAgentRunner(AgentRunnerOptions{Agent: ag, Logger: s.cfg.Logger, Clock: s.cfg.Clock})
	if err != nil {
		s.warn("runner error", err)
		return
	}
	state.mu.Lock()
	state.runner = runner
	state.mu.Unlock()

	if err := sc.SetTyping(ctx, true); err != nil {
		s.warn("typing error", err)
	}
	if err := sc.SetWorking(ctx, true); err != nil {
		s.warn("working error", err)
	}

	prompt := buildPromptText(event, user)
	result, runErr := runner.Run(ctx, sc, prompt)
	if runErr != nil {
		s.warn("run error", runErr)
	}

	state.mu.Lock()
	wasStopRequest := state.stopRequest
	state.mu.Unlock()
	if result.StopReason == "aborted" && wasStopRequest {
		_, _ = s.cfg.Connector.PostMessage(context.Background(), event.Channel, "_Stopped_")
	}
}

func (s *Server) getState(channelID string) *channelState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.channels[channelID]
	if !ok {
		state = &channelState{store: s.cfg.Store}
		s.channels[channelID] = state
	}
	return state
}

func (s *Server) getQueue(channelID string) *channelQueue {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.queues[channelID]
	if !ok {
		q = newChannelQueue()
		s.queues[channelID] = q
	}
	return q
}

func (s *Server) warn(stage string, err error) {
	if s.cfg.Logger == nil || err == nil {
		return
	}
	_, _ = fmt.Fprintf(s.cfg.Logger, "[mom] %s: %v\n", stage, err)
}

// channelQueue is a tiny per-channel FIFO that runs jobs sequentially.
type channelQueue struct {
	mu      sync.Mutex
	queue   []func()
	running bool
}

func newChannelQueue() *channelQueue {
	return &channelQueue{}
}

func (q *channelQueue) size() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.queue)
}

func (q *channelQueue) enqueue(fn func()) {
	q.mu.Lock()
	q.queue = append(q.queue, fn)
	if q.running {
		q.mu.Unlock()
		return
	}
	q.running = true
	q.mu.Unlock()
	go q.drain()
}

func (q *channelQueue) drain() {
	for {
		q.mu.Lock()
		if len(q.queue) == 0 {
			q.running = false
			q.mu.Unlock()
			return
		}
		fn := q.queue[0]
		q.queue = q.queue[1:]
		q.mu.Unlock()
		safeRun(fn)
	}
}

func safeRun(fn func()) {
	defer func() {
		_ = recover()
	}()
	fn()
}

func buildPromptText(event SlackEvent, user SlackUser) string {
	name := user.UserName
	if name == "" {
		name = user.DisplayName
	}
	if name == "" {
		name = event.User
	}
	header := "[" + name + "]: "
	body := strings.TrimSpace(event.Text)
	prompt := header + body
	if len(event.Attachments) > 0 {
		var b strings.Builder
		b.WriteString(prompt)
		b.WriteString("\n\n<slack_attachments>\n")
		for _, att := range event.Attachments {
			b.WriteString(att.Original)
			b.WriteString("\t")
			b.WriteString(att.Local)
			b.WriteString("\n")
		}
		b.WriteString("</slack_attachments>\n")
		prompt = b.String()
	}
	return prompt
}
