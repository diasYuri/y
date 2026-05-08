package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/yuri/y/pkg/agent/compaction"
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/tools"
)

// State describes the current phase of an agent run.
type State string

const (
	StateIdle            State = "idle"
	StateSelectingModel  State = "selecting_model"
	StateRequestingModel State = "requesting_model"
	StateStreaming       State = "streaming"
	StateExecutingTools  State = "executing_tools"
	StateCompleted       State = "completed"
	StateCanceled        State = "canceled"
	StateFailed          State = "failed"
)

// ToolExecutionMode controls whether a tool batch runs sequentially or in
// parallel.
//
// In parallel mode the agent waits for every tool call in the batch to
// finish before propagating the first error it sees. Peers are NOT cancelled
// when one tool fails — they continue to run and have their results
// recorded. Callers that need fail-fast cancellation across the batch
// should run their tools in sequential mode (or wrap their handlers to
// cooperate via the shared run context).
type ToolExecutionMode string

const (
	ToolExecutionSequential ToolExecutionMode = "sequential"
	ToolExecutionParallel   ToolExecutionMode = "parallel"
)

// EventKind identifies a state-machine notification.
type EventKind string

const (
	EventStateChanged EventKind = "state_changed"
	EventTurnStarted  EventKind = "turn_started"
	EventTurnEnded    EventKind = "turn_ended"
	EventTextDelta    EventKind = "text_delta"
	EventToolStarted  EventKind = "tool_started"
	EventToolEnded    EventKind = "tool_ended"
	EventCompleted    EventKind = "completed"
)

// Event is emitted by the agent as it progresses through the loop.
type Event struct {
	Kind       EventKind
	State      State
	Turn       int
	Message    ai.Message
	ToolCall   ai.ToolCall
	ToolResult ai.ToolResult
	Usage      ai.Usage
	TextDelta  string
	Err        error
}

// EventSink receives state-machine events.
type EventSink func(Event)

// Provider describes the subset of providers.Provider used by the agent loop.
type Provider interface {
	ID() string
	Models(context.Context) ([]ai.Model, error)
	Stream(context.Context, providers.StreamRequest) (providers.EventStream, error)
}

// ToolRegistry describes the subset of pkg/tools.Registry used by the agent loop.
type ToolRegistry interface {
	List() []tools.ToolDescriptor
	Handle(context.Context, tools.ToolRequest) (tools.ToolResponse, error)
	GetExecutionMode(name string) tools.ExecutionMode
}

// RunResult summarizes a completed or failed run.
type RunResult struct {
	Messages   []ai.Message
	Usage      ai.Usage
	Turns      int
	State      State
	StopReason ai.StopReason
	Model      ai.Model
}

// Option configures an Agent.
type Option func(*Agent)

// Agent runs the provider/tool loop and keeps the transcript between calls.
type Agent struct {
	mu                sync.Mutex
	provider          Provider
	registry          ToolRegistry
	model             ai.Model
	systemPrompt      string
	workspaceRoot     string
	maxTurns          int
	toolMode          ToolExecutionMode
	onEvent           EventSink
	transcript        []ai.Message
	state             State
	compactor         *compaction.Compactor
	compactionEnabled bool
	compacting        bool // true while an async compaction is in flight

	beforeToolCall  ToolCallHook
	afterToolCall   ToolCallHook
	sessionID       string
	thinkingBudgets map[ai.ThinkingLevel]int64

	// Steering queue: messages injected mid-run.
	steeringMu    sync.Mutex
	steeringQueue []ai.Message

	// Follow-up queue: messages queued for the next run.
	followUpMu    sync.Mutex
	followUpQueue []ai.Message

	// Abort support.
	abortMu   sync.Mutex
	abortFunc context.CancelFunc

	// Multiple event sinks.
	sinksMu    sync.RWMutex
	sinks      map[uint64]EventSink
	nextSinkID uint64

	// Hooks for the request lifecycle.
	beforeRequest BeforeRequestHook
	afterRequest  AfterRequestHook
	onError       ErrorHook

	// Default StreamOptions merged into every request unless overridden
	// per-call via RunOptions.StreamOptions.
	streamDefaults providers.StreamOptions

	// Tool execution tuning.
	toolConcurrency int
	toolTimeout     time.Duration

	// Retry configuration applied to recoverable provider failures.
	maxRetries    int
	maxRetryDelay time.Duration

	// Recovery context: when a run fails with a recoverable error, we
	// stash the messages that need to be re-played on Recover() so that
	// callers can transparently resume the loop.
	recoverableErr error

	// Optional logger for usage-origin diagnostics. nil means "discard".
	logger Logger

	// Optional callback for usage-origin notifications.
	usageObserver UsageObserver

	// Active per-call RunOptions, set during RunWithOptions.
	runOptsMu     sync.Mutex
	activeRunOpts *RunOptions
}

// ToolCallHook is called before or after a tool executes.
// The returned error blocks execution (for before) or is logged (for after).
// For before hooks result is nil; for after hooks it contains the tool result.
type ToolCallHook func(ctx context.Context, call ai.ToolCall, result *ai.ToolResult) error

const defaultMaxTurns = 32

// ErrRetry is a sentinel returned by [ErrorHook] to request that the failing
// step be retried once before propagating an error. The agent honours up to
// [WithMaxRetries] retries per step, after which the original error is
// returned.
var ErrRetry = errors.New("agent: retry requested")

// errBeforeRequestSwallowed signals that a BeforeRequest hook returned an
// error that the OnError hook decided to swallow. The agent treats it as a
// no-op turn (no message appended).
var errBeforeRequestSwallowed = errors.New("agent: before-request error swallowed")

// invokeOnError calls the OnError hook (if any). When the hook returns nil,
// the loop is asked to swallow the error; the caller should treat the step
// as recoverable. When the hook returns [ErrRetry], the caller should retry
// the step. Any other return replaces the original error.
func (a *Agent) invokeOnError(ctx context.Context, phase ErrorPhase, err error) error {
	if err == nil {
		return nil
	}
	a.mu.Lock()
	hook := a.onError
	a.mu.Unlock()
	if hook == nil {
		return err
	}
	return hook(ctx, phase, err)
}

// New creates a new agent with the supplied provider and tool registry.
func New(provider Provider, registry ToolRegistry, opts ...Option) *Agent {
	a := &Agent{
		provider: provider,
		registry: registry,
		maxTurns: defaultMaxTurns,
		toolMode: ToolExecutionParallel,
		state:    StateIdle,
		sinks:    make(map[uint64]EventSink),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(a)
		}
	}
	if a.maxTurns <= 0 {
		a.maxTurns = defaultMaxTurns
	}
	if a.toolMode == "" {
		a.toolMode = ToolExecutionParallel
	}
	return a
}

// WithModel sets the provider model used for future runs.
func WithModel(model ai.Model) Option {
	return func(a *Agent) {
		a.model = model
	}
}

// WithSystemPrompt sets the system prompt forwarded to the provider.
func WithSystemPrompt(systemPrompt string) Option {
	return func(a *Agent) {
		a.systemPrompt = systemPrompt
	}
}

// WithWorkspaceRoot sets the workspace root forwarded to tools.
func WithWorkspaceRoot(workspaceRoot string) Option {
	return func(a *Agent) {
		a.workspaceRoot = workspaceRoot
	}
}

// WithTranscript seeds the transcript used for the next run.
func WithTranscript(messages ...ai.Message) Option {
	return func(a *Agent) {
		a.transcript = append([]ai.Message(nil), messages...)
	}
}

// WithMaxTurns sets the maximum number of assistant/tool iterations.
func WithMaxTurns(maxTurns int) Option {
	return func(a *Agent) {
		a.maxTurns = maxTurns
	}
}

// WithToolExecutionMode sets the tool batch execution strategy.
func WithToolExecutionMode(mode ToolExecutionMode) Option {
	return func(a *Agent) {
		a.toolMode = mode
	}
}

// WithBeforeToolCall registers a hook called before each tool execution.
// If the hook returns an error, the tool is skipped and the error is reported.
func WithBeforeToolCall(hook ToolCallHook) Option {
	return func(a *Agent) {
		a.beforeToolCall = hook
	}
}

// WithAfterToolCall registers a hook called after each tool execution.
// The hook receives the tool call and response; errors are logged but not fatal.
func WithAfterToolCall(hook ToolCallHook) Option {
	return func(a *Agent) {
		a.afterToolCall = hook
	}
}

// WithSessionID sets the session identifier forwarded to providers for
// prompt-cache aware backends.
func WithSessionID(id string) Option {
	return func(a *Agent) {
		a.sessionID = id
	}
}

// WithThinkingBudgets sets per-level reasoning budgets. Providers that
// support reasoning use the budget for the selected ThinkingLevel.
func WithThinkingBudgets(budgets map[ai.ThinkingLevel]int64) Option {
	return func(a *Agent) {
		if budgets != nil {
			a.thinkingBudgets = budgets
		}
	}
}

// WithEventSink registers a callback that receives state-machine events.
func WithEventSink(sink EventSink) Option {
	return func(a *Agent) {
		a.onEvent = sink
	}
}

// WithCompaction enables transcript compaction when usage exceeds the
// configured threshold.
func WithCompaction(enabled bool) Option {
	return func(a *Agent) {
		a.compactionEnabled = enabled
	}
}

// WithCompactor sets a custom compactor. If nil, a default compactor is used.
func WithCompactor(c *compaction.Compactor) Option {
	return func(a *Agent) {
		a.compactor = c
	}
}

// WithBeforeRequest registers a hook called immediately before every provider
// Stream call. The hook may rewrite the outgoing request or short-circuit it
// by returning a non-nil [HookedResponse].
func WithBeforeRequest(hook BeforeRequestHook) Option {
	return func(a *Agent) {
		a.beforeRequest = hook
	}
}

// WithAfterRequest registers a hook called after every provider Stream call,
// including the synthetic short-circuited path. The hook may transform the
// returned error.
func WithAfterRequest(hook AfterRequestHook) Option {
	return func(a *Agent) {
		a.afterRequest = hook
	}
}

// WithOnError registers a hook called whenever the loop sees an error. The
// hook may classify the error and request a retry by returning [ErrRetry].
func WithOnError(hook ErrorHook) Option {
	return func(a *Agent) {
		a.onError = hook
	}
}

// WithStreamDefaults sets default [providers.StreamOptions] to merge into
// every request. Per-call overrides supplied via [RunOptions] take precedence
// over defaults; defaults take precedence over the agent's built-in fields
// (SessionID, ThinkingBudgets) only when they are non-zero.
func WithStreamDefaults(opts providers.StreamOptions) Option {
	return func(a *Agent) {
		a.streamDefaults = opts
	}
}

// WithToolConcurrency caps the number of tool calls executed in parallel
// when the agent runs in [ToolExecutionParallel] mode. Zero or negative
// values mean "unlimited" (the historical default).
func WithToolConcurrency(n int) Option {
	return func(a *Agent) {
		if n < 0 {
			n = 0
		}
		a.toolConcurrency = n
	}
}

// WithToolTimeout sets a per-call timeout applied to every tool execution.
// Zero means no per-call timeout (only the outer context controls duration).
//
// When a tool call exceeds the timeout, the agent records a tool-result
// error message in the transcript instead of failing the run. The model is
// then free to retry the same tool with the same arguments on the next turn,
// and only the global [WithMaxTurns] cap protects against an infinite
// timeout-retry loop. Callers that want stricter protection should pair
// WithToolTimeout with a small WithMaxTurns or implement de-duplication via
// [WithBeforeToolCall].
func WithToolTimeout(d time.Duration) Option {
	return func(a *Agent) {
		if d < 0 {
			d = 0
		}
		a.toolTimeout = d
	}
}

// WithMaxRetries sets the number of automatic retries applied to recoverable
// provider/tool errors. Defaults to 0 (no retries).
func WithMaxRetries(n int) Option {
	return func(a *Agent) {
		if n < 0 {
			n = 0
		}
		a.maxRetries = n
	}
}

// WithMaxRetryDelay caps the exponential backoff delay between retries.
// Defaults to 5s when retries are enabled.
func WithMaxRetryDelay(d time.Duration) Option {
	return func(a *Agent) {
		if d < 0 {
			d = 0
		}
		a.maxRetryDelay = d
	}
}

// WithLogger sets a logger used for non-fatal diagnostics (e.g., token
// estimate fallback, retry decisions). nil silences logging.
func WithLogger(l Logger) Option {
	return func(a *Agent) {
		a.logger = l
	}
}

// WithUsageObserver sets a callback invoked once per assistant turn with the
// observed [ai.Usage] and its [UsageOrigin] (reported vs estimated).
func WithUsageObserver(obs UsageObserver) Option {
	return func(a *Agent) {
		a.usageObserver = obs
	}
}

// State returns the agent's current lifecycle state.
func (a *Agent) State() State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

// Transcript returns a snapshot copy of the accumulated transcript.
func (a *Agent) Transcript() []ai.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ai.Message(nil), a.transcript...)
}

// Reset replaces the transcript and returns the agent to the idle state.
func (a *Agent) Reset(messages ...ai.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.transcript = append([]ai.Message(nil), messages...)
	a.state = StateIdle
}

// Run appends a user prompt and executes the full agent loop.
func (a *Agent) Run(ctx context.Context, prompt string) (RunResult, error) {
	if prompt != "" {
		return a.RunMessages(ctx, ai.Message{
			Role:      ai.RoleUser,
			Content:   []ai.ContentBlock{{Type: ai.ContentText, Text: prompt}},
			Timestamp: time.Now().UTC(),
		})
	}
	return a.RunMessages(ctx)
}

// RunWithOptions is like [Agent.Run] but accepts per-call [RunOptions].
// Per-call StreamOptions take precedence over [WithStreamDefaults] which in
// turn take precedence over the agent's built-in defaults.
func (a *Agent) RunWithOptions(ctx context.Context, prompt string, opts RunOptions) (RunResult, error) {
	messages := append([]ai.Message{}, opts.ExtraMessages...)
	if prompt != "" {
		messages = append(messages, ai.Message{
			Role:      ai.RoleUser,
			Content:   []ai.ContentBlock{{Type: ai.ContentText, Text: prompt}},
			Timestamp: time.Now().UTC(),
		})
	}
	a.runOptsMu.Lock()
	a.activeRunOpts = &opts
	a.runOptsMu.Unlock()
	defer func() {
		a.runOptsMu.Lock()
		a.activeRunOpts = nil
		a.runOptsMu.Unlock()
	}()
	return a.RunMessages(ctx, messages...)
}

// Continue resumes the agent loop from the current transcript without
// adding a new user message. Useful when the model stopped early
// (e.g., max_tokens) and the caller wants it to keep going. If the agent
// is in [StateFailed] with a recoverable error, Continue acts like
// [Agent.Recover] — it retries the failed step. Otherwise it simply
// re-enters the loop on the existing transcript.
func (a *Agent) Continue(ctx context.Context) (RunResult, error) {
	a.mu.Lock()
	state := a.state
	recoverable := a.recoverableErr
	a.mu.Unlock()
	if state == StateFailed && recoverable != nil {
		return a.Recover(ctx)
	}
	return a.RunMessages(ctx)
}

// Recover attempts to resume from [StateFailed] when the most recent
// failure was classified as recoverable (typically a transient provider
// error tagged via the [ErrorHook] or sentinel [ErrRetry]). It clears
// the recoverable-error marker before re-running so callers can detect
// progress by checking the returned [RunResult].
func (a *Agent) Recover(ctx context.Context) (RunResult, error) {
	a.mu.Lock()
	if a.state != StateFailed {
		a.mu.Unlock()
		return RunResult{}, fmt.Errorf("agent: cannot recover from state %q", a.state)
	}
	if a.recoverableErr == nil {
		a.mu.Unlock()
		return RunResult{}, errors.New("agent: last failure was not classified as recoverable")
	}
	a.recoverableErr = nil
	a.state = StateIdle
	a.mu.Unlock()
	return a.RunMessages(ctx)
}

// Steer injects a user message into the current run. If the agent is idle,
// the message is queued for the next run. If the agent is running, it is
// appended to the steering queue and will be injected before the next
// assistant request.
func (a *Agent) Steer(messages ...ai.Message) {
	a.steeringMu.Lock()
	defer a.steeringMu.Unlock()
	a.steeringQueue = append(a.steeringQueue, cloneMessages(messages)...)
}

// FollowUp queues messages for the next run after the current one completes.
func (a *Agent) FollowUp(messages ...ai.Message) {
	a.followUpMu.Lock()
	defer a.followUpMu.Unlock()
	a.followUpQueue = append(a.followUpQueue, cloneMessages(messages)...)
}

// Abort cancels the current run if one is in progress.
func (a *Agent) Abort() {
	a.abortMu.Lock()
	defer a.abortMu.Unlock()
	if a.abortFunc != nil {
		a.abortFunc()
		a.abortFunc = nil
	}
}

// Subscribe registers an additional event sink. All sinks receive every event.
// The returned function removes the sink when called; it is safe to invoke
// from any goroutine and is a no-op after the first call.
//
// Long-lived servers MUST call the returned Unsubscribe function (typically
// via defer) to avoid leaking sinks.
func (a *Agent) Subscribe(sink EventSink) (unsubscribe func()) {
	if sink == nil {
		return func() {}
	}
	a.sinksMu.Lock()
	a.nextSinkID++
	id := a.nextSinkID
	if a.sinks == nil {
		a.sinks = make(map[uint64]EventSink)
	}
	a.sinks[id] = sink
	a.sinksMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			a.sinksMu.Lock()
			delete(a.sinks, id)
			a.sinksMu.Unlock()
		})
	}
}

// RunMessages appends messages and executes the full agent loop.
func (a *Agent) RunMessages(ctx context.Context, messages ...ai.Message) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Wrap context for Abort support.
	runCtx, cancel := context.WithCancel(ctx)
	a.abortMu.Lock()
	a.abortFunc = cancel
	a.abortMu.Unlock()
	defer func() {
		a.abortMu.Lock()
		a.abortFunc = nil
		a.abortMu.Unlock()
		cancel()
	}()

	a.mu.Lock()
	if len(messages) > 0 {
		a.transcript = append(a.transcript, cloneMessages(messages)...)
	}
	a.mu.Unlock()

	result := RunResult{
		State: StateIdle,
	}

	model, err := a.resolveModel(runCtx)
	if err != nil {
		finalState := finalStateForError(runCtx, err)
		a.setState(finalState)
		return a.snapshotResult(result, finalState), err
	}
	result.Model = model

	turns := 0
	for {
		if err := runCtx.Err(); err != nil {
			a.setState(StateCanceled)
			return a.snapshotResult(result, StateCanceled), err
		}
		if turns >= a.maxTurns {
			a.setState(StateFailed)
			err := fmt.Errorf("agent exceeded max turns (%d)", a.maxTurns)
			return a.snapshotResult(result, StateFailed), err
		}

		// Inject any steering messages before the next assistant request.
		a.steeringMu.Lock()
		steering := a.steeringQueue
		a.steeringQueue = nil
		a.steeringMu.Unlock()
		if len(steering) > 0 {
			a.mu.Lock()
			a.transcript = append(a.transcript, steering...)
			a.mu.Unlock()
		}

		a.emit(Event{Kind: EventTurnStarted, Turn: turns + 1, State: StateRequestingModel})

		assistant, usage, stopReason, err := a.requestAssistantWithRetry(runCtx, model, turns+1)
		if err != nil {
			if errors.Is(err, errBeforeRequestSwallowed) {
				// BeforeRequest hook failed but OnError swallowed it.
				// Treat as no-op: skip this turn but stay alive.
				continue
			}
			finalState := finalStateForError(runCtx, err)
			a.setState(finalState)
			result.Usage = addUsage(result.Usage, usage)
			if finalState == StateFailed && isRecoverable(err) {
				a.mu.Lock()
				a.recoverableErr = err
				a.mu.Unlock()
			}
			return a.snapshotResult(result, finalState), err
		}

		turns++
		result.Turns = turns
		result.Usage = addUsage(result.Usage, usage)
		result.StopReason = stopReason
		a.appendTranscript(assistant)
		a.emit(Event{Kind: EventTurnEnded, Turn: turns, State: StateStreaming, Message: assistant, Usage: usage})

		// Trigger compaction if enabled and usage is high.
		a.maybeCompact(runCtx)

		if len(assistant.ToolCalls) == 0 {
			result.StopReason = ai.StopReasonStop
			// If steering messages arrived during this turn, continue.
			a.steeringMu.Lock()
			hasSteering := len(a.steeringQueue) > 0
			steering := a.steeringQueue
			a.steeringQueue = nil
			a.steeringMu.Unlock()
			if hasSteering {
				a.mu.Lock()
				a.transcript = append(a.transcript, steering...)
				a.mu.Unlock()
				continue
			}
			break
		}

		a.setState(StateExecutingTools)
		toolResults, err := a.executeToolCalls(runCtx, assistant.ToolCalls)
		if err != nil {
			finalState := finalStateForError(runCtx, err)
			a.setState(finalState)
			if finalState == StateFailed && isRecoverable(err) {
				a.mu.Lock()
				a.recoverableErr = err
				a.mu.Unlock()
			}
			return a.snapshotResult(result, finalState), err
		}
		for _, toolResult := range toolResults {
			a.appendTranscript(toolResult)
		}
	}

	// Inject follow-up messages for the next run.
	a.followUpMu.Lock()
	followUp := a.followUpQueue
	a.followUpQueue = nil
	a.followUpMu.Unlock()
	if len(followUp) > 0 {
		a.mu.Lock()
		a.transcript = append(a.transcript, followUp...)
		a.mu.Unlock()
	}

	a.setState(StateCompleted)
	a.emit(Event{Kind: EventCompleted, State: StateCompleted, Turn: turns})
	return a.snapshotResult(result, StateCompleted), nil
}

func (a *Agent) resolveModel(ctx context.Context) (ai.Model, error) {
	a.mu.Lock()
	if a.model.ID != "" {
		model := a.model
		a.mu.Unlock()
		return model, nil
	}
	provider := a.provider
	a.mu.Unlock()

	if provider == nil {
		return ai.Model{}, errors.New("agent provider is nil")
	}

	a.setState(StateSelectingModel)
	models, err := provider.Models(ctx)
	if err != nil {
		return ai.Model{}, err
	}
	if len(models) == 0 {
		return ai.Model{}, errors.New("provider returned no models")
	}

	model := models[0]
	a.mu.Lock()
	if a.model.ID == "" {
		a.model = model
	}
	a.mu.Unlock()
	return model, nil
}

// requestAssistantWithRetry wraps requestAssistant with exponential-backoff
// retry honouring [WithMaxRetries] and the [ErrorHook] decision.
func (a *Agent) requestAssistantWithRetry(ctx context.Context, model ai.Model, turn int) (ai.Message, ai.Usage, ai.StopReason, error) {
	a.mu.Lock()
	maxRetries := a.maxRetries
	maxRetryDelay := a.maxRetryDelay
	logger := a.logger
	a.mu.Unlock()
	if maxRetryDelay <= 0 {
		maxRetryDelay = 5 * time.Second
	}

	delay := 100 * time.Millisecond
	for attempt := 0; ; attempt++ {
		msg, usage, stop, err := a.requestAssistant(ctx, model, turn)
		if err == nil {
			return msg, usage, stop, nil
		}

		// Context cancellation is never retried.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return msg, usage, stop, err
		}

		// Determine whether to retry.
		retry := false
		if errors.Is(err, ErrRetry) {
			retry = true
		} else if attempt < maxRetries && isTransient(err) {
			retry = true
		}
		if !retry {
			return msg, usage, stop, err
		}
		if attempt >= maxRetries {
			return msg, usage, stop, err
		}

		if logger != nil {
			logger.Logf("agent: retrying provider request after error (attempt=%d, err=%v)", attempt+1, err)
		}

		// Determine the wait. Start with the exponential backoff value, then
		// honour any provider-supplied Retry-After (e.g. Anthropic's 429 header)
		// by taking the larger of the two so we never re-issue the request
		// before the server's hint elapses.
		wait := delay
		var rl *providers.RateLimitError
		if errors.As(err, &rl) && rl.RetryAfter > wait {
			wait = rl.RetryAfter
			if logger != nil {
				logger.Logf("agent: honouring Retry-After hint of %s before next attempt", rl.RetryAfter)
			}
		}

		// Exponential backoff with cap.
		select {
		case <-ctx.Done():
			return msg, usage, stop, ctx.Err()
		case <-time.After(wait):
		}
		delay *= 2
		if delay > maxRetryDelay {
			delay = maxRetryDelay
		}
	}
}

// isTransient returns true when the error is heuristically retryable
// (network errors, EOF, generic "temporary" errors). Hooks can extend this
// classification by returning [ErrRetry] from [ErrorHook].
//
// The classification recognizes the typed errors exported by
// [providers]:
//
//   - *providers.RateLimitError → always transient
//   - *providers.NetworkError   → transient when StatusCode is 0 (transport
//     failure) or >=500 (server-side); 4xx network errors are permanent
//   - *providers.AuthError      → permanent (never transient)
//   - *providers.ContextOverflowError → permanent (never transient)
//
// Generic io.EOF / io.ErrUnexpectedEOF and errors implementing the
// Temporary() bool interface are also treated as transient for back-compat.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	var rl *providers.RateLimitError
	if errors.As(err, &rl) {
		return true
	}
	var ne *providers.NetworkError
	if errors.As(err, &ne) {
		return ne.StatusCode == 0 || ne.StatusCode >= 500
	}
	var ae *providers.AuthError
	if errors.As(err, &ae) {
		return false
	}
	var ce *providers.ContextOverflowError
	if errors.As(err, &ce) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	type temporary interface{ Temporary() bool }
	var tmp temporary
	if errors.As(err, &tmp) && tmp.Temporary() {
		return true
	}
	return false
}

// isRecoverable returns true when [Recover] (or [Continue]) can sensibly
// retry the failed step. Currently aligned with [isTransient] plus explicit
// retry signals.
func isRecoverable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrRetry) {
		return true
	}
	return isTransient(err)
}

func (a *Agent) requestAssistant(ctx context.Context, model ai.Model, turn int) (ai.Message, ai.Usage, ai.StopReason, error) {
	a.mu.Lock()
	provider := a.provider
	registry := a.registry
	systemPrompt := a.systemPrompt
	transcript := cloneMessages(a.transcript)
	sessionID := a.sessionID
	thinkingBudgets := a.thinkingBudgets
	streamDefaults := a.streamDefaults
	beforeRequest := a.beforeRequest
	afterRequest := a.afterRequest
	logger := a.logger
	usageObs := a.usageObserver
	a.mu.Unlock()

	a.runOptsMu.Lock()
	var perCall providers.StreamOptions
	if a.activeRunOpts != nil {
		perCall = a.activeRunOpts.Stream
	}
	a.runOptsMu.Unlock()

	if provider == nil {
		return ai.Message{}, ai.Usage{}, ai.StopReasonStop, errors.New("agent provider is nil")
	}

	builtin := providers.StreamOptions{
		SessionID:       sessionID,
		ThinkingBudgets: thinkingBudgets,
	}
	effectiveOpts := mergeStreamOptions(builtin, streamDefaults, perCall)

	request := providers.StreamRequest{
		Model: model,
		Context: ai.Context{
			SystemPrompt: systemPrompt,
			Messages:     transcript,
			Tools:        toolDescriptors(registry),
		},
		Options: effectiveOpts,
	}

	if beforeRequest != nil {
		hooked, err := beforeRequest(ctx, &request)
		if err != nil {
			err = a.invokeOnError(ctx, ErrorPhaseRequest, err)
			if err == nil {
				return ai.Message{}, ai.Usage{}, ai.StopReasonStop, errBeforeRequestSwallowed
			}
			return ai.Message{}, ai.Usage{}, ai.StopReasonStop, err
		}
		if hooked != nil {
			msg := hooked.Message
			if msg.Timestamp.IsZero() {
				msg.Timestamp = time.Now().UTC()
			}
			if msg.Role == "" {
				msg.Role = ai.RoleAssistant
			}
			stopReason := hooked.StopReason
			if stopReason == "" {
				stopReason = ai.StopReasonStop
			}
			// Notify the usage observer for the short-circuited path so callers
			// that meter cache hits via WithUsageObserver still see them.
			if usageObs != nil {
				usageObs(UsageReported, hooked.Usage)
			}
			if afterRequest != nil {
				if hookErr := afterRequest(ctx, request, msg, hooked.Usage, nil); hookErr != nil {
					return ai.Message{}, hooked.Usage, stopReason, hookErr
				}
			}
			return msg, hooked.Usage, stopReason, nil
		}
	}

	a.setState(StateRequestingModel)
	stream, err := provider.Stream(ctx, request)
	if err != nil {
		err = a.invokeOnError(ctx, ErrorPhaseRequest, err)
		if afterRequest != nil {
			err = afterRequest(ctx, request, ai.Message{}, ai.Usage{}, err)
		}
		return ai.Message{}, ai.Usage{}, ai.StopReasonStop, err
	}
	defer stream.Close()

	a.setState(StateStreaming)

	builder := newAssistantBuilder()
	usage := ai.Usage{}
	stopReason := ai.StopReasonStop
	usageReported := false
	for {
		if err := ctx.Err(); err != nil {
			return ai.Message{}, usage, stopReason, err
		}
		event, err := stream.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			err = a.invokeOnError(ctx, ErrorPhaseRequest, err)
			if afterRequest != nil {
				err = afterRequest(ctx, request, ai.Message{}, usage, err)
			}
			return ai.Message{}, usage, stopReason, err
		}

		switch e := event.(type) {
		case ai.TextDelta:
			a.emit(Event{Kind: EventTextDelta, State: StateStreaming, Turn: turn, TextDelta: e.Text})
			builder.addText(e.Text)
		case ai.ToolCallEvent:
			builder.addToolCall(e)
		case ai.UsageEvent:
			usage = addUsage(usage, e.Usage)
			usageReported = true
		case ai.StopEvent:
			stopReason = e.Reason
		case ai.ErrorEvent:
			err := a.invokeOnError(ctx, ErrorPhaseRequest, e)
			if afterRequest != nil {
				err = afterRequest(ctx, request, ai.Message{}, usage, err)
			}
			return ai.Message{}, usage, stopReason, err
		}
	}

	message, err := builder.build()
	if err != nil {
		return ai.Message{}, usage, stopReason, err
	}
	if message.Timestamp.IsZero() {
		message.Timestamp = time.Now().UTC()
	}
	if message.Role != ai.RoleAssistant {
		message.Role = ai.RoleAssistant
	}
	// Fallback token estimation when provider does not report usage.
	origin := UsageReported
	if !usageReported || usage.OutputTokens == 0 {
		// TODO(providers-gap): consider exposing a Provider capability so the
		// agent can detect "no usage reported" intent vs "unknown" upstream.
		usage.OutputTokens = estimateTokens(message)
		origin = UsageEstimated
		if logger != nil {
			logger.Logf("agent: usage fell back to char-based estimate (turn=%d, model=%s, est=%d)",
				turn, model.ID, usage.OutputTokens)
		}
	}
	if usageObs != nil {
		usageObs(origin, usage)
	}

	if afterRequest != nil {
		if hookErr := afterRequest(ctx, request, message, usage, nil); hookErr != nil {
			return ai.Message{}, usage, stopReason, hookErr
		}
	}

	return message, usage, stopReason, nil
}

func estimateTokens(msg ai.Message) int64 {
	var chars int64
	for _, block := range msg.Content {
		chars += int64(len(block.Text))
	}
	for _, tc := range msg.ToolCalls {
		chars += int64(len(tc.Name))
		chars += int64(len(tc.Arguments))
	}
	// Heuristic: 4 ASCII chars ~ 1 token, 1 non-ASCII char ~ 1 token.
	return chars/4 + 1
}

func (a *Agent) executeToolCalls(ctx context.Context, toolCalls []ai.ToolCall) ([]ai.Message, error) {
	a.mu.Lock()
	registry := a.registry
	workspaceRoot := a.workspaceRoot
	mode := a.toolMode
	concurrency := a.toolConcurrency
	a.mu.Unlock()

	if registry == nil || len(toolCalls) == 0 {
		return nil, nil
	}

	a.setState(StateExecutingTools)

	results := make([]ai.Message, len(toolCalls))

	// If global mode is sequential, or any tool in the batch requests
	// sequential execution, run everything sequentially.
	anySequential := mode == ToolExecutionSequential
	if !anySequential {
		for _, tc := range toolCalls {
			if registry.GetExecutionMode(tc.Name) == tools.ExecutionSequential {
				anySequential = true
				break
			}
		}
	}

	if anySequential {
		for i, toolCall := range toolCalls {
			message, err := a.executeToolCall(ctx, registry, workspaceRoot, toolCall)
			if err != nil {
				return nil, err
			}
			results[i] = message
		}
	} else {
		// Optionally cap concurrency.
		var sem chan struct{}
		if concurrency > 0 {
			sem = make(chan struct{}, concurrency)
		}
		var wg sync.WaitGroup
		errCh := make(chan error, len(toolCalls))
		for i, toolCall := range toolCalls {
			i, toolCall := i, toolCall
			wg.Add(1)
			go func() {
				defer wg.Done()
				if sem != nil {
					select {
					case sem <- struct{}{}:
					case <-ctx.Done():
						errCh <- ctx.Err()
						return
					}
					defer func() { <-sem }()
				}
				message, err := a.executeToolCall(ctx, registry, workspaceRoot, toolCall)
				if err != nil {
					errCh <- err
					return
				}
				results[i] = message
			}()
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			if err != nil {
				return nil, err
			}
		}
	}

	return results, nil
}

func (a *Agent) executeToolCall(ctx context.Context, registry ToolRegistry, workspaceRoot string, toolCall ai.ToolCall) (ai.Message, error) {
	if err := ctx.Err(); err != nil {
		return ai.Message{}, err
	}

	// Apply per-call tool timeout if configured.
	a.mu.Lock()
	toolTimeout := a.toolTimeout
	a.mu.Unlock()
	if toolTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, toolTimeout)
		defer cancel()
	}

	// before hook
	if a.beforeToolCall != nil {
		if err := a.beforeToolCall(ctx, toolCall, nil); err != nil {
			message := toolResultMessage(toolCall, tools.ToolResponse{}, err)
			a.emit(Event{Kind: EventToolEnded, State: StateExecutingTools, ToolCall: toolCall, ToolResult: *message.ToolResult, Err: err})
			return message, nil
		}
	}

	a.emit(Event{Kind: EventToolStarted, State: StateExecutingTools, ToolCall: toolCall})
	resp, err := registry.Handle(ctx, tools.ToolRequest{
		ID:            toolCall.ID,
		Name:          toolCall.Name,
		Arguments:     toolCall.Arguments,
		WorkspaceRoot: workspaceRoot,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			// Surface tool timeout differently from outer cancel: a per-call
			// timeout produces an error message in the transcript instead of
			// failing the run, unless the outer context is also done.
			if toolTimeout > 0 && (errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)) {
				message := toolResultMessage(toolCall, tools.ToolResponse{}, err)
				a.emit(Event{Kind: EventToolEnded, State: StateExecutingTools, ToolCall: toolCall, ToolResult: *message.ToolResult, Err: err})
				if a.afterToolCall != nil {
					_ = a.afterToolCall(ctx, toolCall, message.ToolResult)
				}
				return message, nil
			}
			return ai.Message{}, err
		}
		// Allow OnError hook to reclassify before failing the call.
		if hookErr := a.invokeOnError(ctx, ErrorPhaseTool, err); hookErr != nil {
			err = hookErr
		}
		message := toolResultMessage(toolCall, tools.ToolResponse{}, err)
		a.emit(Event{Kind: EventToolEnded, State: StateExecutingTools, ToolCall: toolCall, ToolResult: *message.ToolResult, Err: err})
		// after hook (with error)
		if a.afterToolCall != nil {
			_ = a.afterToolCall(ctx, toolCall, message.ToolResult)
		}
		return message, nil
	}

	message := toolResultMessage(toolCall, resp, nil)
	a.emit(Event{Kind: EventToolEnded, State: StateExecutingTools, ToolCall: toolCall, ToolResult: *message.ToolResult})
	// after hook (success)
	if a.afterToolCall != nil {
		_ = a.afterToolCall(ctx, toolCall, message.ToolResult)
	}
	return message, nil
}

func toolDescriptors(registry ToolRegistry) []ai.Tool {
	if registry == nil {
		return nil
	}
	descriptors := registry.List()
	if len(descriptors) == 0 {
		return nil
	}
	out := make([]ai.Tool, 0, len(descriptors))
	for _, desc := range descriptors {
		out = append(out, ai.Tool{
			Name:        desc.Name,
			Description: desc.Description,
			InputSchema: append([]byte(nil), desc.InputSchema...),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

type assistantBuilder struct {
	text      bytes.Buffer
	toolCalls map[int]*pendingToolCall
}

type pendingToolCall struct {
	call      ai.ToolCall
	arguments bytes.Buffer
	seenDelta bool
	complete  bool
}

func newAssistantBuilder() *assistantBuilder {
	return &assistantBuilder{
		toolCalls: make(map[int]*pendingToolCall),
	}
}

func (b *assistantBuilder) addText(text string) {
	if text == "" {
		return
	}
	b.text.WriteString(text)
}

func (b *assistantBuilder) addToolCall(event ai.ToolCallEvent) {
	call := b.toolCalls[event.ContentIndex]
	if call == nil {
		call = &pendingToolCall{}
		b.toolCalls[event.ContentIndex] = call
	}
	if event.ToolCall.ID != "" {
		call.call.ID = event.ToolCall.ID
	}
	if event.ToolCall.Name != "" {
		call.call.Name = event.ToolCall.Name
	}
	if len(event.ToolCall.Arguments) > 0 {
		call.call.Arguments = append([]byte(nil), event.ToolCall.Arguments...)
	}
	if len(event.ArgumentsDelta) > 0 {
		call.arguments.Write(event.ArgumentsDelta)
		call.seenDelta = true
	}
	if event.Complete {
		call.complete = true
		if len(call.call.Arguments) == 0 && call.seenDelta {
			call.call.Arguments = append([]byte(nil), call.arguments.Bytes()...)
		}
	}
}

func (b *assistantBuilder) build() (ai.Message, error) {
	toolCalls := make([]ai.ToolCall, 0, len(b.toolCalls))
	if len(b.toolCalls) > 0 {
		indexes := make([]int, 0, len(b.toolCalls))
		for index := range b.toolCalls {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		for _, index := range indexes {
			pending := b.toolCalls[index]
			call, err := pending.finalize()
			if err != nil {
				return ai.Message{}, err
			}
			toolCalls = append(toolCalls, call)
		}
	}

	content := make([]ai.ContentBlock, 0, 1)
	if text := b.text.String(); text != "" {
		content = append(content, ai.ContentBlock{Type: ai.ContentText, Text: text})
	}
	return ai.Message{
		Role:      ai.RoleAssistant,
		Content:   content,
		ToolCalls: toolCalls,
		Timestamp: time.Now().UTC(),
	}, nil
}

func (p *pendingToolCall) finalize() (ai.ToolCall, error) {
	if !p.complete && p.seenDelta {
		return ai.ToolCall{}, errors.New("incomplete tool call stream")
	}
	if p.call.Name == "" {
		return ai.ToolCall{}, errors.New("tool call missing name")
	}
	if len(p.call.Arguments) == 0 && p.seenDelta {
		p.call.Arguments = append([]byte(nil), p.arguments.Bytes()...)
	}
	if len(p.call.Arguments) == 0 && !p.complete {
		return ai.ToolCall{}, errors.New("tool call missing complete event")
	}
	return p.call, nil
}

func toolResultMessage(call ai.ToolCall, resp tools.ToolResponse, err error) ai.Message {
	result := ai.ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    make([]ai.ContentBlock, 0, len(resp.Content)),
		IsError:    resp.IsError || err != nil,
		Details:    append([]byte(nil), resp.Details...),
	}
	for _, block := range resp.Content {
		result.Content = append(result.Content, ai.ContentBlock{
			Type: ai.ContentText,
			Text: block.Text,
		})
	}

	if err != nil {
		result.Content = errorContent(err)
		if details := toolErrorDetails(err); len(details) > 0 {
			result.Details = details
		}
	} else if len(result.Content) == 0 {
		result.Content = []ai.ContentBlock{{Type: ai.ContentText, Text: ""}}
	}

	return ai.Message{
		Role:       ai.RoleToolResult,
		ToolResult: &result,
		Timestamp:  time.Now().UTC(),
	}
}

type toolErrorEnvelope struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func toolErrorDetails(err error) []byte {
	var toolErr *tools.Error
	if errors.As(err, &toolErr) {
		raw, marshalErr := json.Marshal(toolErrorEnvelope{
			Code:    toolErr.Code,
			Message: toolErr.Error(),
		})
		if marshalErr == nil {
			return raw
		}
	}
	raw, marshalErr := json.Marshal(toolErrorEnvelope{Message: err.Error()})
	if marshalErr != nil {
		return []byte(`{"message":"unknown tool error"}`)
	}
	return raw
}

func errorContent(err error) []ai.ContentBlock {
	if err == nil {
		return nil
	}
	return []ai.ContentBlock{{Type: ai.ContentText, Text: err.Error()}}
}

func addUsage(dst, src ai.Usage) ai.Usage {
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.CacheReadTokens += src.CacheReadTokens
	dst.CacheWriteTokens += src.CacheWriteTokens
	dst.TotalTokens += src.TotalTokens
	dst.Cost.Input += src.Cost.Input
	dst.Cost.Output += src.Cost.Output
	dst.Cost.CacheRead += src.Cost.CacheRead
	dst.Cost.CacheWrite += src.Cost.CacheWrite
	dst.Cost.Total += src.Cost.Total
	return dst
}

func finalStateForError(ctx context.Context, err error) State {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return StateCanceled
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); errors.Is(ctxErr, context.Canceled) || errors.Is(ctxErr, context.DeadlineExceeded) {
			return StateCanceled
		}
	}
	return StateFailed
}

func cloneMessages(messages []ai.Message) []ai.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]ai.Message, len(messages))
	for i, message := range messages {
		out[i] = cloneMessage(message)
	}
	return out
}

func cloneMessage(message ai.Message) ai.Message {
	cloned := ai.Message{
		Role:      message.Role,
		Timestamp: message.Timestamp,
	}
	if len(message.Content) > 0 {
		cloned.Content = make([]ai.ContentBlock, len(message.Content))
		copy(cloned.Content, message.Content)
		for i := range cloned.Content {
			if len(cloned.Content[i].ImageData) > 0 {
				cloned.Content[i].ImageData = append([]byte(nil), cloned.Content[i].ImageData...)
			}
			if len(cloned.Content[i].ProviderMetadata) > 0 {
				cloned.Content[i].ProviderMetadata = append([]byte(nil), cloned.Content[i].ProviderMetadata...)
			}
		}
	}
	if len(message.ToolCalls) > 0 {
		cloned.ToolCalls = make([]ai.ToolCall, len(message.ToolCalls))
		copy(cloned.ToolCalls, message.ToolCalls)
		for i := range cloned.ToolCalls {
			if len(cloned.ToolCalls[i].Arguments) > 0 {
				cloned.ToolCalls[i].Arguments = append([]byte(nil), cloned.ToolCalls[i].Arguments...)
			}
		}
	}
	if message.ToolResult != nil {
		cloned.ToolResult = &ai.ToolResult{
			ToolCallID: message.ToolResult.ToolCallID,
			ToolName:   message.ToolResult.ToolName,
			IsError:    message.ToolResult.IsError,
			Details:    append([]byte(nil), message.ToolResult.Details...),
		}
		if len(message.ToolResult.Content) > 0 {
			cloned.ToolResult.Content = make([]ai.ContentBlock, len(message.ToolResult.Content))
			copy(cloned.ToolResult.Content, message.ToolResult.Content)
			for i := range cloned.ToolResult.Content {
				if len(cloned.ToolResult.Content[i].ImageData) > 0 {
					cloned.ToolResult.Content[i].ImageData = append([]byte(nil), cloned.ToolResult.Content[i].ImageData...)
				}
				if len(cloned.ToolResult.Content[i].ProviderMetadata) > 0 {
					cloned.ToolResult.Content[i].ProviderMetadata = append([]byte(nil), cloned.ToolResult.Content[i].ProviderMetadata...)
				}
			}
		}
	}
	return cloned
}

func (a *Agent) appendTranscript(message ai.Message) {
	a.mu.Lock()
	a.transcript = append(a.transcript, cloneMessage(message))
	a.mu.Unlock()
}

func (a *Agent) setState(state State) {
	a.mu.Lock()
	a.state = state
	snapshot := state
	a.mu.Unlock()
	a.emit(Event{Kind: EventStateChanged, State: snapshot})
}

func (a *Agent) emit(event Event) {
	a.mu.Lock()
	sink := a.onEvent
	a.mu.Unlock()
	if sink != nil {
		sink(event)
	}
	a.sinksMu.RLock()
	sinks := make([]EventSink, 0, len(a.sinks))
	for _, s := range a.sinks {
		sinks = append(sinks, s)
	}
	a.sinksMu.RUnlock()
	for _, s := range sinks {
		if s != nil {
			s(event)
		}
	}
}

func (a *Agent) maybeCompact(ctx context.Context) {
	if !a.compactionEnabled {
		return
	}
	a.mu.Lock()
	if a.compacting {
		a.mu.Unlock()
		return
	}
	provider := a.provider
	model := a.model
	transcript := a.transcript
	compactor := a.compactor
	a.mu.Unlock()

	if provider == nil || model.ContextWindow <= 0 {
		return
	}
	if compactor == nil {
		compactor = compaction.NewCompactor()
	}

	// Quick check: if we are well below the threshold, skip the goroutine.
	tokens := compaction.EstimateTranscriptTokens(transcript)
	tokens = compaction.AdjustedEstimate(tokens, string(model.Provider))
	limit := int64(float64(model.ContextWindow) * compactor.Threshold)
	if tokens <= limit {
		return
	}

	// Launch async compaction.
	a.mu.Lock()
	a.compacting = true
	a.mu.Unlock()

	go func() {
		defer func() {
			a.mu.Lock()
			a.compacting = false
			a.mu.Unlock()
		}()

		rewritten, didCompact, err := compactor.MaybeCompact(ctx, transcript, provider, model)
		if err != nil {
			return
		}
		if !didCompact {
			return
		}
		a.mu.Lock()
		a.transcript = rewritten
		a.mu.Unlock()
	}()
}

func (a *Agent) snapshotResult(result RunResult, state State) RunResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	result.Messages = cloneMessages(a.transcript)
	result.State = state
	result.Model = a.model
	return result
}
