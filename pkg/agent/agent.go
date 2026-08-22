package agent

import (
	"context"
	"sync"
	"time"

	"github.com/yuri/y/pkg/agent/compaction"
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

const defaultMaxTurns = 32

// Agent runs the provider/tool loop and keeps the transcript between calls.
// The public methods are implemented in focused files; this type owns the
// shared state and the dependencies used by those collaborators.
type Agent struct {
	// runMu serializes runs because the transcript and Abort handle belong to
	// the agent, not to an individual caller.
	runMu sync.Mutex

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
	transcriptVersion uint64
	state             State
	compactor         *compaction.Compactor
	compactionEnabled bool
	compacting        bool

	beforeToolCall  ToolCallHook
	afterToolCall   ToolCallHook
	sessionID       string
	thinkingBudgets map[ai.ThinkingLevel]int64

	// Messages injected during a run and messages queued for the next run have
	// independent ownership and synchronization.
	steeringMu    sync.Mutex
	steeringQueue []ai.Message
	followUpMu    sync.Mutex
	followUpQueue []ai.Message

	abortMu   sync.Mutex
	abortFunc context.CancelFunc

	sinksMu    sync.RWMutex
	sinks      map[uint64]EventSink
	nextSinkID uint64

	beforeRequest BeforeRequestHook
	afterRequest  AfterRequestHook
	onError       ErrorHook

	streamDefaults providers.StreamOptions

	toolConcurrency int
	toolTimeout     time.Duration

	maxRetries    int
	maxRetryDelay time.Duration

	recoverableErr error
	logger         Logger
	usageObserver  UsageObserver
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
