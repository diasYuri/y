package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/agent/compaction"
	"github.com/yuri/y/pkg/ai"
	ycontext "github.com/yuri/y/pkg/context"
	runtimeextensions "github.com/yuri/y/pkg/extensions"
	pmemory "github.com/yuri/y/pkg/memory"
	"github.com/yuri/y/pkg/tools"
)

// Config controls the native memory extension.
type Config struct {
	Enabled          bool
	Mode             string
	Profile          string
	Backend          string
	Directory        string
	MaxItems         int
	MaxContextTokens int64
	MaxReadBytes     int64
	AutoExtract      bool
	DedicatedTools   bool
	FailOpen         bool
	QueueSize        int
	WorkerID         string
	TenantID         string
	WorkspaceID      string
	ProjectID        string
}

// DefaultConfig enables local, human-readable memory with conservative
// limits. Extraction remains in suggest mode until explicitly configured.
func DefaultConfig() Config {
	return Config{Enabled: true, Mode: ModeSuggest, Profile: "local", Backend: "filesystem", MaxItems: 5, MaxContextTokens: 1500, MaxReadBytes: 65536, AutoExtract: true, DedicatedTools: true, FailOpen: true, QueueSize: 128}
}

// ConfigFromSettings adapts the generic extension namespace to this
// extension's schema. The core configuration package never needs to know
// these keys or their types.
func ConfigFromSettings(settings map[string]string, lookup func(string) string) (Config, error) {
	config := DefaultConfig()
	for key, raw := range settings {
		value, err := extensionSettingString(raw)
		if err != nil {
			return Config{}, fmt.Errorf("memory setting %q: %w", key, err)
		}
		switch key {
		case "enabled":
			config.Enabled, err = strconv.ParseBool(value)
		case "mode":
			config.Mode = value
		case "profile":
			config.Profile = value
		case "backend":
			config.Backend = value
		case "directory":
			config.Directory = value
		case "max_items":
			config.MaxItems, err = strconv.Atoi(value)
		case "max_context_tokens":
			config.MaxContextTokens, err = strconv.ParseInt(value, 10, 64)
		case "max_read_bytes":
			config.MaxReadBytes, err = strconv.ParseInt(value, 10, 64)
		case "auto_extract":
			config.AutoExtract, err = strconv.ParseBool(value)
		case "dedicated_tools":
			config.DedicatedTools, err = strconv.ParseBool(value)
		case "fail_open":
			config.FailOpen, err = strconv.ParseBool(value)
		case "queue_size":
			config.QueueSize, err = strconv.Atoi(value)
		case "worker_id":
			config.WorkerID = value
		case "tenant_id":
			config.TenantID = value
		case "workspace_id":
			config.WorkspaceID = value
		case "project_id":
			config.ProjectID = value
		default:
			return Config{}, fmt.Errorf("unknown memory setting %q", key)
		}
		if err != nil {
			return Config{}, fmt.Errorf("memory setting %q: %w", key, err)
		}
	}
	if lookup == nil {
		lookup = os.Getenv
	}
	if value := strings.TrimSpace(lookup("Y_EXTENSION_MEMORY")); value != "" {
		var err error
		config.Enabled, err = strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("Y_EXTENSION_MEMORY: %w", err)
		}
	}
	if value := strings.TrimSpace(lookup("Y_EXTENSION_MEMORY_ENABLED")); value != "" {
		var err error
		config.Enabled, err = strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("Y_EXTENSION_MEMORY_ENABLED: %w", err)
		}
	}
	if err := ValidateConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func extensionSettingString(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return strconv.Unquote(value)
	}
	return value, nil
}

// ValidateConfig validates only memory-specific settings. It is exported so
// application composition can validate a configured extension without
// constructing storage or starting a worker.
func ValidateConfig(config Config) error {
	if config.MaxItems < 0 || config.MaxContextTokens < 0 || config.MaxReadBytes < 0 || config.QueueSize < 0 {
		return errors.New("memory limits and queue size must be non-negative")
	}
	if config.Mode != "" && config.Mode != ModeExplicit && config.Mode != ModeSuggest && config.Mode != ModeAutomatic {
		return fmt.Errorf("unsupported memory mode %q", config.Mode)
	}
	if config.Profile != "" && config.Profile != "local" && config.Profile != "ephemeral" && config.Profile != "stateless" {
		return fmt.Errorf("unsupported memory profile %q", config.Profile)
	}
	if config.Backend != "" && config.Backend != "filesystem" && config.Backend != "noop" && config.Backend != "memory" && config.Backend != "remote" {
		return fmt.Errorf("unsupported memory backend %q", config.Backend)
	}
	return nil
}

// Option configures dependency injection for tests and deployment adapters.
type Option func(*options)
type options struct {
	config    Config
	store     pmemory.Store
	queue     pmemory.JobQueue
	extractor pmemory.Extractor
}

func WithConfig(config Config) Option         { return func(value *options) { value.config = config } }
func WithStore(store pmemory.Store) Option    { return func(value *options) { value.store = store } }
func WithQueue(queue pmemory.JobQueue) Option { return func(value *options) { value.queue = queue } }
func WithExtractor(extractor pmemory.Extractor) Option {
	return func(value *options) { value.extractor = extractor }
}

// Extension composes the context source, lifecycle hooks, explicit tools, and
// asynchronous capture worker without exposing storage implementation details.
type Extension struct {
	mu            sync.Mutex
	config        Config
	store         pmemory.Store
	source        *Source
	worker        *Worker
	explicit      bool
	lastRun       agent.TurnContext
	explicitByRun map[string]bool
	lastRunByRun  map[string]agent.TurnContext
	captureSeq    uint64
	closed        bool
}

// New constructs a memory extension from the selected profile.
func New(opts ...Option) (*Extension, error) {
	value := options{config: DefaultConfig()}
	for _, opt := range opts {
		if opt != nil {
			opt(&value)
		}
	}
	config := value.config
	if config.Mode == "" {
		config.Mode = ModeSuggest
	}
	if config.Profile == "" {
		config.Profile = "local"
	}
	if config.Backend == "" {
		config.Backend = "filesystem"
	}
	if config.MaxItems <= 0 {
		config.MaxItems = 5
	}
	if config.MaxContextTokens <= 0 {
		config.MaxContextTokens = 1500
	}
	if config.MaxReadBytes <= 0 {
		config.MaxReadBytes = 65536
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 128
	}
	if err := ValidateConfig(config); err != nil {
		return nil, err
	}
	store := value.store
	if store == nil {
		switch {
		case !config.Enabled || config.Profile == "stateless" || config.Backend == "noop" || config.Backend == "memory" || config.Profile == "ephemeral":
			store = NoopStore{}
		case config.Backend == "filesystem":
			// The public extension cannot assume a filesystem implementation. A
			// host that selects local persistence injects its private adapter.
			store = NoopStore{}
		default:
			return nil, errors.New("memory backend requires an injected Store")
		}
	}
	ext := &Extension{config: config, store: store, explicitByRun: make(map[string]bool), lastRunByRun: make(map[string]agent.TurnContext)}
	ext.source = NewSource(store, SourceOptions{Name: "memory", MaxItems: config.MaxItems, MaxContextTokens: config.MaxContextTokens, MaxReadBytes: config.MaxReadBytes, FailOpen: config.FailOpen})
	if config.Enabled && config.AutoExtract {
		ext.worker = NewWorker(WorkerOptions{Mode: config.Mode, Queue: value.queue, Extractor: value.extractor, Store: store, QueueSize: config.QueueSize, WorkerID: config.WorkerID})
		ext.worker.Start()
	}
	return ext, nil
}

// Source returns the context source used for pre-request recall.
func (e *Extension) Source() ycontext.ContextSource {
	if e == nil {
		return NewSource(NoopStore{}, SourceOptions{FailOpen: true})
	}
	return e.source
}

// Store returns the injected source of truth for explicit integrations.
func (e *Extension) Store() pmemory.Store {
	if e == nil {
		return NoopStore{}
	}
	return e.store
}

// Hooks returns process-local lifecycle adapters. Hooks are intentionally not
// serialised in agent snapshots and must be re-registered after handoff.
func (e *Extension) Hooks() agent.RuntimeHooks {
	return agent.RuntimeHooks{BeforeMessage: e.beforeMessage, BeforeTurn: e.beforeTurn, AfterCompaction: e.afterCompaction, OnSession: e.onSession, AfterRun: e.afterRun}
}

// RegisterTools adds explicit memory tools to the supplied registry.
func (e *Extension) RegisterTools(reg *tools.Registry) error {
	if e == nil || reg == nil {
		return errors.New("memory extension and registry are required")
	}
	if !e.config.Enabled || !e.config.DedicatedTools {
		return nil
	}
	return registerTools(reg, e)
}

// ID identifies the extension to a generic host.
func (e *Extension) ID() string { return "memory" }

// Install connects the extension to the generic runtime host.
func (e *Extension) Install(host *runtimeextensions.Host) error {
	if e == nil || host == nil {
		return errors.New("memory extension and host are required")
	}
	if !e.config.Enabled {
		return nil
	}
	if err := host.AddSource(e.Source()); err != nil {
		return err
	}
	host.AddHooks(e.Hooks())
	return e.RegisterTools(host.Registry)
}

// Close drains local workers before returning. Durable remote queues remain
// recoverable through their lease and retry semantics if the caller deadline
// expires before the current job can finish.
func (e *Extension) Close(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	worker := e.worker
	e.mu.Unlock()
	if worker != nil {
		return worker.Close(ctx)
	}
	return nil
}

func (e *Extension) beforeMessage(ctx context.Context, message *ai.Message) error {
	if e == nil || message == nil || message.Role != ai.RoleUser {
		return nil
	}
	var text strings.Builder
	for _, block := range message.Content {
		text.WriteString(block.Text)
		text.WriteByte(' ')
	}
	value := strings.ToLower(text.String())
	explicit := strings.Contains(value, "remember") || strings.Contains(value, "save this") || strings.Contains(value, "save to memory") || strings.Contains(value, "memorize") || strings.Contains(value, "don't forget") || strings.Contains(value, "forget") || strings.Contains(value, "remove from memory") || strings.Contains(value, "lembre") || strings.Contains(value, "salve na memória") || strings.Contains(value, "esqueça") || strings.Contains(value, "apague da memória")
	e.mu.Lock()
	identity, identified := agent.RuntimeIdentityFromContext(ctx)
	if identified && identity.RunID != "" {
		if e.explicitByRun == nil {
			e.explicitByRun = make(map[string]bool)
		}
		e.explicitByRun[identity.RunID] = explicit
	} else {
		e.explicit = explicit
	}
	e.mu.Unlock()
	return nil
}

func (e *Extension) beforeTurn(_ context.Context, turn agent.TurnContext) error {
	if e != nil {
		e.mu.Lock()
		e.lastRun = turn
		if turn.RunID != "" {
			if e.lastRunByRun == nil {
				e.lastRunByRun = make(map[string]agent.TurnContext)
			}
			e.lastRunByRun[turn.RunID] = turn
		}
		e.mu.Unlock()
	}
	return nil
}
func (e *Extension) afterCompaction(_ context.Context, _ compaction.Result, _ error) {}

func (e *Extension) onSession(_ context.Context, event agent.SessionEvent) error {
	if e != nil {
		e.mu.Lock()
		e.lastRun.RunID, e.lastRun.SessionID = event.RunID, event.SessionID
		if event.RunID != "" {
			if e.lastRunByRun == nil {
				e.lastRunByRun = make(map[string]agent.TurnContext)
			}
			e.lastRunByRun[event.RunID] = e.lastRun
		}
		e.mu.Unlock()
	}
	return nil
}

func (e *Extension) afterRun(ctx context.Context, result agent.RunResult) error {
	if e == nil || e.worker == nil || result.State == agent.StateWaitingApproval || len(result.Messages) == 0 {
		return nil
	}
	e.mu.Lock()
	run := e.lastRun
	config := e.config
	identity, identified := agent.RuntimeIdentityFromContext(ctx)
	if identified && identity.RunID != "" {
		if value, ok := e.lastRunByRun[identity.RunID]; ok {
			run = value
		} else {
			run.RunID = identity.RunID
			run.SessionID = identity.SessionID
		}
	}
	e.captureSeq++
	captureSeq := e.captureSeq
	e.mu.Unlock()
	transcript, err := json.Marshal(result.Messages)
	if err != nil {
		return err
	}
	transcript = []byte(RedactText(string(transcript)))
	capturedAt := time.Now().UTC()
	request := pmemory.ExtractionRequest{
		JobID:       fmt.Sprintf("capture:%s:%d:%d", run.RunID, capturedAt.UnixNano(), captureSeq),
		SessionID:   run.SessionID,
		RunID:       run.RunID,
		ProjectID:   config.ProjectID,
		TenantID:    config.TenantID,
		WorkspaceID: config.WorkspaceID,
		Transcript:  transcript,
		CapturedAt:  capturedAt,
	}
	return e.worker.Enqueue(ctx, request)
}
