package mom

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/ai"
)

// SlackContext is the per-channel adapter passed to the agent runner. It hides
// the Slack message bookkeeping (postMessage, updateMessage, threads) so the
// runner does not need to know whether the connector is real or fake.
type SlackContext struct {
	Channel     string
	ChannelName string
	UserID      string
	UserName    string
	Connector   Connector
	Store       *ChannelStore
	logger      io.Writer
	maxMain     int
	maxThread   int
	working     bool
	workingHint string
	mu          sync.Mutex
	mainTS      string
	mainText    string
	threadTS    []string
}

// SlackContextOptions configures a SlackContext.
type SlackContextOptions struct {
	Channel     string
	ChannelName string
	UserID      string
	UserName    string
	Connector   Connector
	Store       *ChannelStore
	Logger      io.Writer
	MaxMain     int
	MaxThread   int
	WorkingHint string
}

// NewSlackContext builds a SlackContext.
func NewSlackContext(opts SlackContextOptions) (*SlackContext, error) {
	if opts.Connector == nil {
		return nil, errors.New("slack context: connector is required")
	}
	maxMain := opts.MaxMain
	if maxMain <= 0 {
		maxMain = 35000
	}
	maxThread := opts.MaxThread
	if maxThread <= 0 {
		maxThread = 20000
	}
	hint := opts.WorkingHint
	if hint == "" {
		hint = " ..."
	}
	return &SlackContext{
		Channel:     opts.Channel,
		ChannelName: opts.ChannelName,
		UserID:      opts.UserID,
		UserName:    opts.UserName,
		Connector:   opts.Connector,
		Store:       opts.Store,
		logger:      opts.Logger,
		maxMain:     maxMain,
		maxThread:   maxThread,
		workingHint: hint,
		working:     true,
	}, nil
}

// Respond appends text to the main reply, truncating if necessary, and either
// posts a new message or updates the existing one.
func (s *SlackContext) Respond(ctx context.Context, text string, shouldLog bool) error {
	s.mu.Lock()
	if s.mainText == "" {
		s.mainText = text
	} else {
		s.mainText = s.mainText + "\n" + text
	}
	s.mainText = truncateMain(s.mainText, s.maxMain)
	display := s.displayLocked()
	s.mu.Unlock()

	if err := s.publishMain(ctx, display); err != nil {
		return err
	}
	if shouldLog && s.Store != nil {
		s.mu.Lock()
		ts := s.mainTS
		s.mu.Unlock()
		if ts != "" {
			if logErr := s.Store.LogBotResponse(s.Channel, text, ts); logErr != nil {
				s.warn("respond log error", logErr)
			}
		}
	}
	return nil
}

// ReplaceMessage replaces the entire main reply text.
func (s *SlackContext) ReplaceMessage(ctx context.Context, text string) error {
	s.mu.Lock()
	s.mainText = truncateMain(text, s.maxMain)
	display := s.displayLocked()
	s.mu.Unlock()
	return s.publishMain(ctx, display)
}

// RespondInThread posts text into the existing thread.
func (s *SlackContext) RespondInThread(ctx context.Context, text string) error {
	s.mu.Lock()
	parent := s.mainTS
	s.mu.Unlock()
	if parent == "" {
		return errors.New("slack context: no main message yet")
	}
	threadText := text
	if s.maxThread > 0 && len(threadText) > s.maxThread {
		threadText = threadText[:s.maxThread-len(threadTruncationNote)] + threadTruncationNote
	}
	ts, err := s.Connector.PostInThread(ctx, s.Channel, parent, threadText)
	if err != nil {
		s.warn("thread error", err)
		return err
	}
	s.mu.Lock()
	s.threadTS = append(s.threadTS, ts)
	s.mu.Unlock()
	return nil
}

// SetTyping reflects an "I am thinking" placeholder in the main message slot.
func (s *SlackContext) SetTyping(ctx context.Context, isTyping bool) error {
	if !isTyping {
		return nil
	}
	s.mu.Lock()
	if s.mainTS != "" || s.mainText != "" {
		s.mu.Unlock()
		return nil
	}
	s.mainText = "_Thinking_"
	display := s.displayLocked()
	s.mu.Unlock()
	return s.publishMain(ctx, display)
}

// UploadFile delegates to the connector.
func (s *SlackContext) UploadFile(ctx context.Context, path, title string) error {
	if s.Connector == nil {
		return errors.New("slack context: connector is nil")
	}
	if err := s.Connector.UploadFile(ctx, s.Channel, path, title); err != nil {
		s.warn("upload error", err)
		return err
	}
	return nil
}

// SetWorking toggles the trailing "..." indicator on the main reply.
func (s *SlackContext) SetWorking(ctx context.Context, working bool) error {
	s.mu.Lock()
	s.working = working
	display := s.displayLocked()
	s.mu.Unlock()
	if err := s.publishMain(ctx, display); err != nil {
		return err
	}
	return nil
}

// DeleteMessage removes the main message and any thread replies.
func (s *SlackContext) DeleteMessage(ctx context.Context) error {
	s.mu.Lock()
	mainTS := s.mainTS
	threadTS := append([]string(nil), s.threadTS...)
	s.threadTS = nil
	s.mainTS = ""
	s.mu.Unlock()

	for i := len(threadTS) - 1; i >= 0; i-- {
		_ = s.Connector.DeleteMessage(ctx, s.Channel, threadTS[i])
	}
	if mainTS != "" {
		if err := s.Connector.DeleteMessage(ctx, s.Channel, mainTS); err != nil {
			return err
		}
	}
	return nil
}

// MainTS returns the current main message ts (mostly for tests).
func (s *SlackContext) MainTS() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mainTS
}

// ThreadTSs returns a copy of recorded thread TSs (mostly for tests).
func (s *SlackContext) ThreadTSs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.threadTS))
	copy(out, s.threadTS)
	return out
}

func (s *SlackContext) publishMain(ctx context.Context, text string) error {
	s.mu.Lock()
	ts := s.mainTS
	s.mu.Unlock()
	if ts == "" {
		newTS, err := s.Connector.PostMessage(ctx, s.Channel, text)
		if err != nil {
			s.warn("post error", err)
			return err
		}
		s.mu.Lock()
		s.mainTS = newTS
		s.mu.Unlock()
		return nil
	}
	if err := s.Connector.UpdateMessage(ctx, s.Channel, ts, text); err != nil {
		s.warn("update error", err)
		return err
	}
	return nil
}

func (s *SlackContext) displayLocked() string {
	if s.working {
		return s.mainText + s.workingHint
	}
	return s.mainText
}

const threadTruncationNote = "\n\n_(truncated)_"
const mainTruncationNote = "\n\n_(message truncated, ask me to elaborate on specific parts)_"

func truncateMain(text string, max int) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	if max <= len(mainTruncationNote) {
		return text[:max]
	}
	return text[:max-len(mainTruncationNote)] + mainTruncationNote
}

func (s *SlackContext) warn(stage string, err error) {
	if s.logger == nil || err == nil {
		return
	}
	_, _ = fmt.Fprintf(s.logger, "[mom] %s: %v\n", stage, err)
}

// AgentRunner orchestrates a single agent run for a Slack message.
type AgentRunner struct {
	Agent      *agent.Agent
	Logger     io.Writer
	clock      Clock
	mu         sync.Mutex
	running    bool
	cancelFunc context.CancelFunc
}

// AgentRunnerOptions configure an AgentRunner.
type AgentRunnerOptions struct {
	Agent  *agent.Agent
	Logger io.Writer
	Clock  Clock
}

// NewAgentRunner builds a new AgentRunner.
func NewAgentRunner(opts AgentRunnerOptions) (*AgentRunner, error) {
	if opts.Agent == nil {
		return nil, errors.New("agent runner: agent is required")
	}
	clock := opts.Clock
	if clock == nil {
		clock = SystemClock()
	}
	return &AgentRunner{
		Agent:  opts.Agent,
		Logger: opts.Logger,
		clock:  clock,
	}, nil
}

// Abort cancels the active run, if any.
func (r *AgentRunner) Abort() {
	r.mu.Lock()
	cancel := r.cancelFunc
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// IsRunning reports whether a run is currently in progress.
func (r *AgentRunner) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// Run executes one agent turn for the supplied prompt and streams responses
// through the SlackContext.
func (r *AgentRunner) Run(ctx context.Context, sc *SlackContext, prompt string) (RunResult, error) {
	if sc == nil {
		return RunResult{}, errors.New("agent runner: slack context is required")
	}
	if r.Agent == nil {
		return RunResult{}, errors.New("agent runner: agent is nil")
	}
	if prompt == "" {
		return RunResult{}, errors.New("agent runner: prompt is empty")
	}

	r.mu.Lock()
	r.running = true
	runCtx, cancel := context.WithCancel(ctx)
	r.cancelFunc = cancel
	r.mu.Unlock()

	defer func() {
		cancel()
		r.mu.Lock()
		r.running = false
		r.cancelFunc = nil
		r.mu.Unlock()
	}()

	result, runErr := r.Agent.Run(runCtx, prompt)
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			return RunResult{StopReason: "aborted"}, nil
		}
		return RunResult{StopReason: "error", ErrorMessage: runErr.Error()}, runErr
	}

	if err := r.publishFinal(ctx, sc, result); err != nil {
		return RunResult{StopReason: "error", ErrorMessage: err.Error()}, err
	}
	return RunResult{StopReason: string(result.StopReason)}, nil
}

func (r *AgentRunner) publishFinal(ctx context.Context, sc *SlackContext, result agent.RunResult) error {
	last := lastAssistantText(result.Messages)
	if last == "" {
		last = "_(no response)_"
	}
	if err := sc.ReplaceMessage(ctx, last); err != nil {
		return err
	}
	return sc.SetWorking(ctx, false)
}

func lastAssistantText(messages []ai.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != ai.RoleAssistant {
			continue
		}
		var b strings.Builder
		for _, block := range msg.Content {
			if block.Type == ai.ContentText && block.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(block.Text)
			}
		}
		if b.Len() == 0 {
			continue
		}
		return b.String()
	}
	return ""
}
