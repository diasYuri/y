package subagents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/policy"
)

const (
	defaultMaxConcurrent       = 4
	defaultMaxQueued           = 32
	defaultMaxChildren         = 16
	defaultMaxDepth            = 1
	defaultTimeout             = 10 * time.Minute
	defaultMaxResultBytes      = 32 << 10
	defaultCompletionTextBytes = 8 << 10
)

var (
	ErrManagerClosed      = errors.New("subagent manager is closed")
	ErrInvalidRequest     = errors.New("invalid subagent request")
	ErrQueueFull          = errors.New("subagent queue is full")
	ErrLimitExceeded      = errors.New("subagent limit exceeded")
	ErrNestedDisabled     = errors.New("nested subagent creation is disabled")
	ErrBackendUnavailable = errors.New("subagent backend is unavailable")
	ErrNotOwned           = errors.New("subagent is not owned by this parent")
	ErrManagerConfig      = errors.New("invalid subagent manager configuration")
	ErrPersistence        = errors.New("subagent persistence failed")
)

// Mode selects the execution backend for a child session.
type Mode string

const (
	ModeEphemeral Mode = "ephemeral"
	ModeDurable   Mode = "durable"
)

// Status is the lifecycle state of a managed child.
type Status string

const (
	StatusQueued          Status = "queued"
	StatusRunning         Status = "running"
	StatusCompleted       Status = "completed"
	StatusFailed          Status = "failed"
	StatusCanceled        Status = "canceled"
	StatusWaitingApproval Status = "waiting_approval"
)

// SpawnRequest describes one isolated child execution. ParentID is the
// caller's run ID and is used as the ownership boundary for lifecycle tools.
// API keys are deliberately absent; providers resolve credentials outside the
// extension or through the durable runner's credential resolver.
type SpawnRequest struct {
	ParentID               string                     `json:"parent_id"`
	Name                   string                     `json:"name,omitempty"`
	Prompt                 string                     `json:"prompt"`
	Mode                   Mode                       `json:"mode,omitempty"`
	Timeout                time.Duration              `json:"timeout,omitempty"`
	Model                  ai.Model                   `json:"model,omitempty"`
	ProviderID             ai.ProviderID              `json:"provider_id,omitempty"`
	SystemPrompt           string                     `json:"system_prompt,omitempty"`
	WorkspaceRoot          string                     `json:"workspace_root,omitempty"`
	WorkspaceID            string                     `json:"workspace_id,omitempty"`
	TenantID               string                     `json:"tenant_id,omitempty"`
	ProjectID              string                     `json:"project_id,omitempty"`
	Identity               policy.Identity            `json:"identity,omitempty"`
	PolicyVersion          string                     `json:"policy_version,omitempty"`
	AuthorizationExpiresAt time.Time                  `json:"authorization_expires_at,omitempty"`
	RequestID              string                     `json:"request_id,omitempty"`
	Approval               *policy.ApprovalResolution `json:"approval,omitempty"`
}

// ChildContext is passed to optional provider, registry, and option factories
// when constructing a child. It contains no API key or live parent transcript.
type ChildContext struct {
	ID       string
	ParentID string
	Request  SpawnRequest
}

// Result is the bounded result exposed to the parent. The full transcript is
// retained only by the Agent or durable runner configured by the host.
type Result struct {
	State      agent.State             `json:"state,omitempty"`
	Text       string                  `json:"text,omitempty"`
	Turns      int                     `json:"turns,omitempty"`
	StopReason ai.StopReason           `json:"stop_reason,omitempty"`
	Usage      ai.Usage                `json:"usage,omitempty"`
	Error      string                  `json:"error,omitempty"`
	Truncated  bool                    `json:"truncated,omitempty"`
	Approval   *policy.ApprovalRequest `json:"approval,omitempty"`
}

// Record is the store-facing lifecycle record for a child.
type Record struct {
	ID         string       `json:"id"`
	ParentID   string       `json:"parent_id"`
	Depth      int          `json:"depth"`
	Name       string       `json:"name"`
	Mode       Mode         `json:"mode"`
	Status     Status       `json:"status"`
	Request    SpawnRequest `json:"request"`
	Result     Result       `json:"result,omitempty"`
	Error      string       `json:"error,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
	StartedAt  time.Time    `json:"started_at,omitempty"`
	FinishedAt time.Time    `json:"finished_at,omitempty"`
	Version    uint64       `json:"version"`
}

// ProviderFactory optionally selects a provider per child. When nil, Config.Provider
// is shared. Providers must support concurrent Stream calls as required by the
// providers.Provider contract.
type ProviderFactory func(context.Context, ChildContext) (agent.Provider, error)

// RegistryFactory optionally creates a child-specific registry. When nil,
// Config.Registry is shared, preserving the host's policy and tool handlers.
type RegistryFactory func(context.Context, ChildContext) (agent.ToolRegistry, error)

// ChildOptionsFactory supplies options for a child without inheriting the
// parent's transcript or hooks implicitly.
type ChildOptionsFactory func(ChildContext) []agent.Option

// ManagerConfig configures a Manager.
type ManagerConfig struct {
	Provider             agent.Provider
	Registry             agent.ToolRegistry
	ProviderFactory      ProviderFactory
	RegistryFactory      RegistryFactory
	DurableRunner        *agent.AgentRunner
	DurableRunnerFactory func(context.Context, ChildContext) (*agent.AgentRunner, error)
	DurableControl       agent.RunControl
	Store                Store
	DefaultMode          Mode
	MaxConcurrent        int
	MaxQueued            int
	MaxChildrenPerParent int
	MaxDepth             int
	DefaultTimeout       time.Duration
	MaxResultBytes       int
	AllowNested          bool
	ChildOptions         ChildOptionsFactory
}

type job struct {
	record          Record
	done            chan struct{}
	ctx             context.Context
	cancel          context.CancelFunc
	runtime         *agent.Agent
	cancelRequested bool
	persistenceErr  error
}

// Manager owns child sessions for one or more parent runs. It is safe for
// tool handlers, hooks, and control callers to use concurrently.
type Manager struct {
	cfg     ManagerConfig
	store   Store
	queue   chan *job
	ctx     context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup

	mu        sync.RWMutex
	jobs      map[string]*job
	followUps map[string]bool
	closed    bool
	changed   chan struct{}
	closeOnce sync.Once
}

// New creates a manager and starts its bounded worker pool.
func New(config ManagerConfig) (*Manager, error) {
	if config.Provider == nil && config.ProviderFactory == nil && config.DurableRunner == nil && config.DurableRunnerFactory == nil {
		return nil, fmt.Errorf("%w: a provider or durable runner is required", ErrManagerConfig)
	}
	if config.DefaultMode == "" {
		config.DefaultMode = ModeEphemeral
	}
	if config.DefaultMode != ModeEphemeral && config.DefaultMode != ModeDurable {
		return nil, fmt.Errorf("%w: unsupported default mode %q", ErrManagerConfig, config.DefaultMode)
	}
	if config.DefaultMode == ModeEphemeral && config.Registry == nil && config.RegistryFactory == nil {
		return nil, fmt.Errorf("%w: ephemeral mode requires a registry", ErrManagerConfig)
	}
	if config.DefaultMode == ModeDurable && config.DurableRunner == nil && config.DurableRunnerFactory == nil {
		return nil, fmt.Errorf("%w: durable mode requires an AgentRunner", ErrManagerConfig)
	}
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = defaultMaxConcurrent
	}
	if config.MaxQueued <= 0 {
		config.MaxQueued = defaultMaxQueued
	}
	if config.MaxChildrenPerParent <= 0 {
		config.MaxChildrenPerParent = defaultMaxChildren
	}
	if config.MaxDepth <= 0 {
		config.MaxDepth = defaultMaxDepth
	}
	if config.DefaultTimeout <= 0 {
		config.DefaultTimeout = defaultTimeout
	}
	if config.MaxResultBytes <= 0 {
		config.MaxResultBytes = defaultMaxResultBytes
	}
	if config.Store == nil {
		if config.DurableRunner != nil || config.DurableRunnerFactory != nil {
			return nil, fmt.Errorf("%w: durable mode requires an explicit metadata store", ErrManagerConfig)
		}
		config.Store = NewMemoryStore()
	}

	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		cfg:       config,
		store:     config.Store,
		queue:     make(chan *job, config.MaxQueued),
		ctx:       ctx,
		cancel:    cancel,
		jobs:      make(map[string]*job),
		followUps: make(map[string]bool),
		changed:   make(chan struct{}),
	}
	for i := 0; i < config.MaxConcurrent; i++ {
		m.workers.Add(1)
		go m.worker()
	}
	return m, nil
}

// Spawn queues a child and returns its queued record. The caller must supply
// ParentID; tool handlers derive it from the current parent run and never
// trust a model-provided parent ID.
func (m *Manager) Spawn(ctx context.Context, request SpawnRequest) (Record, error) {
	if m == nil {
		return Record{}, ErrManagerClosed
	}
	ctx = nonNilContext(ctx)
	if err := contextErr(ctx); err != nil {
		return Record{}, err
	}
	request.ParentID = strings.TrimSpace(request.ParentID)
	request.Name = strings.TrimSpace(request.Name)
	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.ParentID == "" || request.Prompt == "" {
		return Record{}, fmt.Errorf("%w: parent ID and prompt are required", ErrInvalidRequest)
	}
	if request.Mode == "" {
		request.Mode = m.cfg.DefaultMode
	}
	if request.Mode != ModeEphemeral && request.Mode != ModeDurable {
		return Record{}, fmt.Errorf("%w: unsupported mode %q", ErrInvalidRequest, request.Mode)
	}
	if request.Mode == ModeEphemeral && m.cfg.Provider == nil && m.cfg.ProviderFactory == nil {
		return Record{}, fmt.Errorf("%w: ephemeral mode requires a provider", ErrBackendUnavailable)
	}
	if request.Mode == ModeDurable && m.cfg.DurableRunner == nil && m.cfg.DurableRunnerFactory == nil {
		return Record{}, fmt.Errorf("%w: durable mode requires an AgentRunner", ErrBackendUnavailable)
	}
	if request.Timeout < 0 {
		return Record{}, fmt.Errorf("%w: timeout cannot be negative", ErrInvalidRequest)
	}
	if request.Timeout == 0 {
		request.Timeout = m.cfg.DefaultTimeout
	}
	if request.Name == "" {
		request.Name = "subagent"
	}
	request.Identity = cloneIdentity(request.Identity)

	id, err := newID()
	if err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	record := Record{
		ID:        id,
		ParentID:  request.ParentID,
		Name:      request.Name,
		Mode:      request.Mode,
		Status:    StatusQueued,
		Request:   cloneRequest(request),
		CreatedAt: now,
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Record{}, ErrManagerClosed
	}
	depth, err := m.validateParentLocked(request.ParentID)
	if err != nil {
		m.mu.Unlock()
		return Record{}, err
	}
	if depth > m.cfg.MaxDepth {
		m.mu.Unlock()
		return Record{}, fmt.Errorf("%w: maximum nesting depth is %d", ErrLimitExceeded, m.cfg.MaxDepth)
	}
	record.Depth = depth
	childCount, err := m.childCountLocked(ctx, request.ParentID)
	if err != nil {
		m.mu.Unlock()
		return Record{}, err
	}
	if childCount >= m.cfg.MaxChildrenPerParent {
		m.mu.Unlock()
		return Record{}, fmt.Errorf("%w: parent %q allows at most %d children", ErrLimitExceeded, request.ParentID, m.cfg.MaxChildrenPerParent)
	}
	if len(m.queue) >= cap(m.queue) {
		m.mu.Unlock()
		return Record{}, ErrQueueFull
	}
	if err := m.store.Create(ctx, record); err != nil {
		m.mu.Unlock()
		return Record{}, err
	}
	record.Version = 1
	childCtx, cancel := context.WithCancel(ctx)
	entry := &job{record: record, done: make(chan struct{}), ctx: childCtx, cancel: cancel}
	m.jobs[id] = entry
	select {
	case m.queue <- entry:
		m.followUps[request.ParentID] = true
		m.signalLocked()
		m.mu.Unlock()
		return cloneRecord(record), nil
	default:
		delete(m.jobs, id)
		cancel()
		_ = m.store.Delete(context.Background(), id)
		m.mu.Unlock()
		return Record{}, ErrQueueFull
	}
}

// Get returns a child owned by parentID.
func (m *Manager) Get(ctx context.Context, parentID, id string) (Record, error) {
	ctx = nonNilContext(ctx)
	record, err := m.loadOwned(ctx, parentID, id)
	if err != nil {
		return Record{}, err
	}
	return cloneRecord(record), nil
}

// List returns all children owned by parentID.
func (m *Manager) List(ctx context.Context, parentID string) ([]Record, error) {
	if m == nil {
		return nil, ErrManagerClosed
	}
	ctx = nonNilContext(ctx)
	if strings.TrimSpace(parentID) == "" {
		return nil, fmt.Errorf("%w: parent ID is required", ErrInvalidRequest)
	}
	records, err := m.store.List(ctx, parentID)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	for i := range records {
		if entry, ok := m.jobs[records[i].ID]; ok && entry.record.ParentID == parentID {
			records[i] = cloneRecord(entry.record)
		}
	}
	m.mu.RUnlock()
	return records, nil
}

// Wait blocks until a child reaches a terminal state or ctx is canceled.
func (m *Manager) Wait(ctx context.Context, parentID, id string) (Record, error) {
	if m == nil {
		return Record{}, ErrManagerClosed
	}
	ctx = nonNilContext(ctx)
	for {
		m.mu.RLock()
		changed := m.changed
		m.mu.RUnlock()
		record, err := m.loadOwned(ctx, parentID, id)
		if err != nil {
			return Record{}, err
		}
		if isTerminal(record.Status) {
			return record, nil
		}

		m.mu.RLock()
		entry := m.jobs[id]
		unchanged := changed == m.changed
		m.mu.RUnlock()
		switch {
		case entry != nil:
			select {
			case <-entry.done:
			case <-ctx.Done():
				return Record{}, ctx.Err()
			}
		case unchanged:
			select {
			case <-changed:
			case <-ctx.Done():
				return Record{}, ctx.Err()
			}
		default:
			continue
		}
	}
}

// WaitParent waits until every child of parentID is terminal. The bool is
// true when at least one non-terminal child was observed and waited for.
func (m *Manager) WaitParent(ctx context.Context, parentID string) (bool, error) {
	if m == nil {
		return false, ErrManagerClosed
	}
	ctx = nonNilContext(ctx)
	waited := false
	for {
		m.mu.RLock()
		changed := m.changed
		m.mu.RUnlock()
		records, err := m.List(ctx, parentID)
		if err != nil {
			return waited, err
		}
		active := false
		for _, record := range records {
			if !isTerminal(record.Status) {
				active = true
				break
			}
		}
		if !active {
			return waited, nil
		}
		waited = true
		m.mu.RLock()
		unchanged := changed == m.changed
		m.mu.RUnlock()
		if !unchanged {
			continue
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return waited, ctx.Err()
		}
	}
}

// Cancel requests cancellation of a child. The local context is canceled
// immediately; a configured durable RunControl receives the same request so
// another worker cannot keep the child alive.
func (m *Manager) Cancel(ctx context.Context, parentID, id string) error {
	if m == nil {
		return ErrManagerClosed
	}
	ctx = nonNilContext(ctx)
	record, err := m.loadOwned(ctx, parentID, id)
	if err != nil {
		return err
	}
	if isTerminal(record.Status) {
		return nil
	}

	m.mu.Lock()
	entry := m.jobs[id]
	if entry == nil && record.Status == StatusWaitingApproval && record.Mode == ModeDurable {
		childCtx, cancel := context.WithCancel(m.ctx)
		entry = &job{record: record, done: make(chan struct{}), ctx: childCtx, cancel: cancel}
		m.jobs[id] = entry
	}
	waitingApproval := entry != nil && entry.record.Status == StatusWaitingApproval
	if entry != nil {
		entry.cancelRequested = true
		if waitingApproval {
			m.finishLocked(entry, StatusCanceled, Result{State: agent.StateCanceled, Error: context.Canceled.Error()})
		}
	}
	m.mu.Unlock()
	if entry != nil && entry.cancel != nil {
		entry.cancel()
	}
	if record.Mode == ModeDurable && m.cfg.DurableControl != nil {
		if err := m.cfg.DurableControl.RequestAbort(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// Resume supplies an approval decision and requeues a child that is waiting
// for approval. Durable children can be resumed after a manager handoff;
// ephemeral children require their in-process runtime to still be available.
func (m *Manager) Resume(ctx context.Context, parentID, id string, resolution policy.ApprovalResolution) error {
	if m == nil {
		return ErrManagerClosed
	}
	ctx = nonNilContext(ctx)
	if err := contextErr(ctx); err != nil {
		return err
	}
	if resolution.State != policy.ApprovalApproved && resolution.State != policy.ApprovalDenied {
		return fmt.Errorf("%w: approval state must be approved or denied", ErrInvalidRequest)
	}
	record, err := m.loadOwned(ctx, parentID, id)
	if err != nil {
		return err
	}
	if record.Status != StatusWaitingApproval {
		return fmt.Errorf("%w: child %q is not waiting for approval", ErrInvalidRequest, id)
	}
	if record.Result.Approval != nil && record.Result.Approval.ApprovalID != "" &&
		resolution.ApprovalID != "" && record.Result.Approval.ApprovalID != resolution.ApprovalID {
		return fmt.Errorf("%w: approval ID does not match pending request", ErrInvalidRequest)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrManagerClosed
	}
	if len(m.queue) >= cap(m.queue) {
		m.mu.Unlock()
		return ErrQueueFull
	}
	entry := m.jobs[id]
	if entry == nil {
		if record.Mode != ModeDurable {
			m.mu.Unlock()
			return fmt.Errorf("%w: ephemeral child runtime is unavailable", ErrBackendUnavailable)
		}
		childCtx, cancel := context.WithCancel(m.ctx)
		entry = &job{record: record, done: make(chan struct{}), ctx: childCtx, cancel: cancel}
		m.jobs[id] = entry
	}
	if entry.record.Status != StatusWaitingApproval {
		m.mu.Unlock()
		return fmt.Errorf("%w: child %q is not waiting for approval", ErrInvalidRequest, id)
	}
	previous := cloneRecord(entry.record)
	resolutionCopy := resolution
	entry.record.Request.Approval = &resolutionCopy
	entry.record.Status = StatusQueued
	entry.record.Result = Result{}
	entry.record.Error = ""
	entry.record.StartedAt = time.Time{}
	entry.record.FinishedAt = time.Time{}
	if err := m.saveLocked(entry); err != nil {
		entry.record = previous
		entry.persistenceErr = err
		entry.record.Error = persistenceError(err).Error()
		m.mu.Unlock()
		return err
	}
	entry.persistenceErr = nil
	if entry.runtime != nil {
		entry.runtime.ResolveApproval(resolution)
	}
	m.queue <- entry
	m.signalLocked()
	m.mu.Unlock()
	return nil
}

// CancelParent requests cancellation of all non-terminal children. It is
// used by AgentOption when the parent run is canceled or fails.
func (m *Manager) CancelParent(ctx context.Context, parentID string) error {
	ctx = nonNilContext(ctx)
	records, err := m.List(ctx, parentID)
	if err != nil {
		return err
	}
	var errs []error
	for _, record := range records {
		if isTerminal(record.Status) {
			continue
		}
		if err := m.Cancel(ctx, parentID, record.ID); err != nil {
			errs = append(errs, fmt.Errorf("cancel %s: %w", record.ID, err))
		}
	}
	return errors.Join(errs...)
}

// Recover requeues non-terminal durable records found in the configured
// store. Running records are treated as interrupted and handed back to the
// AgentRunner, whose lease and checkpoint rules decide whether continuation is
// safe. Waiting approvals remain paused until the host resolves them.
func (m *Manager) Recover(ctx context.Context) error {
	if m == nil {
		return ErrManagerClosed
	}
	ctx = nonNilContext(ctx)
	records, err := m.store.List(ctx, "")
	if err != nil {
		return err
	}
	var errs []error
	for _, record := range records {
		if record.Mode != ModeDurable || isTerminal(record.Status) || record.Status == StatusWaitingApproval {
			continue
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return ErrManagerClosed
		}
		if _, exists := m.jobs[record.ID]; exists {
			m.mu.Unlock()
			continue
		}
		if len(m.queue) >= cap(m.queue) {
			m.mu.Unlock()
			errs = append(errs, fmt.Errorf("%s: %w", record.ID, ErrQueueFull))
			continue
		}
		if record.Status == StatusRunning {
			record.Status = StatusQueued
			record.StartedAt = time.Time{}
			if err := m.store.Save(ctx, record.ID, record.Version, record); err != nil {
				m.mu.Unlock()
				errs = append(errs, fmt.Errorf("%s: %w", record.ID, err))
				continue
			}
			record.Version++
		}
		childCtx, cancel := context.WithCancel(m.ctx)
		entry := &job{record: record, done: make(chan struct{}), ctx: childCtx, cancel: cancel}
		m.jobs[record.ID] = entry
		m.queue <- entry
		m.signalLocked()
		m.mu.Unlock()
	}
	return errors.Join(errs...)
}

// Close cancels active children, marks queued children canceled, and waits
// for all fixed-pool workers to stop. It is idempotent.
func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	ctx = nonNilContext(ctx)
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.cancel()
		for _, entry := range m.jobs {
			if isTerminal(entry.record.Status) {
				continue
			}
			entry.cancelRequested = true
			if entry.record.Status == StatusQueued || entry.record.Status == StatusWaitingApproval {
				m.finishLocked(entry, StatusCanceled, Result{State: agent.StateCanceled, Error: ErrManagerClosed.Error()})
			} else if entry.cancel != nil {
				entry.cancel()
			}
		}
		m.signalLocked()
		m.mu.Unlock()
	})

	done := make(chan struct{})
	go func() {
		m.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) worker() {
	defer m.workers.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case entry := <-m.queue:
			if entry == nil {
				return
			}
			m.run(entry)
		}
	}
}

func (m *Manager) run(entry *job) {
	m.mu.Lock()
	if isTerminal(entry.record.Status) {
		m.mu.Unlock()
		return
	}
	if entry.ctx.Err() != nil || m.closed {
		m.finishLocked(entry, StatusCanceled, Result{State: agent.StateCanceled, Error: context.Canceled.Error()})
		m.mu.Unlock()
		return
	}
	entry.record.Status = StatusRunning
	entry.record.StartedAt = time.Now().UTC()
	if err := m.saveLocked(entry); err != nil {
		m.finishLocked(entry, StatusFailed, Result{State: agent.StateFailed, Error: err.Error()})
		m.mu.Unlock()
		return
	}
	m.signalLocked()
	m.mu.Unlock()

	ctx := entry.ctx
	cancel := func() {}
	if entry.record.Request.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, entry.record.Request.Timeout)
	}
	defer cancel()

	result, err := m.execute(ctx, entry)
	status := statusForResult(ctx, entry, result, err)
	if err != nil && result.Error == "" {
		result.Error = err.Error()
	}
	if result.State == "" {
		result.State = stateForStatus(status)
	}

	m.mu.Lock()
	m.finishLocked(entry, status, result)
	m.mu.Unlock()
}

func (m *Manager) execute(ctx context.Context, entry *job) (Result, error) {
	request := cloneRequest(entry.record.Request)
	request.Identity = childIdentity(request.Identity, entry.record.ID)
	child := ChildContext{ID: entry.record.ID, ParentID: request.ParentID, Request: request}
	if request.Mode == ModeDurable {
		runner := m.cfg.DurableRunner
		var err error
		if m.cfg.DurableRunnerFactory != nil {
			runner, err = m.cfg.DurableRunnerFactory(ctx, child)
			if err != nil {
				return Result{State: agent.StateFailed}, err
			}
		}
		if runner == nil {
			return Result{State: agent.StateFailed}, ErrBackendUnavailable
		}
		response, err := runner.Run(ctx, agent.RunRequest{
			RunID:                  entry.record.ID,
			SessionID:              entry.record.ID,
			TenantID:               request.TenantID,
			WorkspaceID:            request.WorkspaceID,
			ProjectID:              request.ProjectID,
			Identity:               cloneIdentity(request.Identity),
			PolicyVersion:          request.PolicyVersion,
			AuthorizationExpiresAt: request.AuthorizationExpiresAt,
			IdempotencyKey:         "subagent:" + entry.record.ID,
			RequestID:              request.RequestID,
			ProviderID:             request.ProviderID,
			ModelID:                request.Model.ID,
			Model:                  request.Model,
			Prompt:                 request.Prompt,
			SystemPrompt:           request.SystemPrompt,
			WorkspaceRoot:          request.WorkspaceRoot,
			Approval:               cloneApprovalResolution(request.Approval),
		})
		return summarize(response.Result, err, m.cfg.MaxResultBytes), err
	}

	provider := m.cfg.Provider
	var err error
	if m.cfg.ProviderFactory != nil {
		provider, err = m.cfg.ProviderFactory(ctx, child)
		if err != nil {
			return Result{State: agent.StateFailed}, err
		}
	}
	if provider == nil {
		return Result{State: agent.StateFailed}, ErrBackendUnavailable
	}
	registry := m.cfg.Registry
	if m.cfg.RegistryFactory != nil {
		registry, err = m.cfg.RegistryFactory(ctx, child)
		if err != nil {
			return Result{State: agent.StateFailed}, err
		}
	}
	if registry == nil {
		return Result{State: agent.StateFailed}, ErrBackendUnavailable
	}

	m.mu.Lock()
	childAgent := entry.runtime
	cancelRequested := entry.cancelRequested
	m.mu.Unlock()
	if childAgent == nil {
		options := make([]agent.Option, 0, 12)
		if m.cfg.ChildOptions != nil {
			options = append(options, m.cfg.ChildOptions(child)...)
		}
		options = append(options,
			agent.WithRunID(entry.record.ID),
			agent.WithRunNonce(entry.record.ID),
			agent.WithSessionID(entry.record.ID),
			agent.WithIdempotencyKey("subagent:"+entry.record.ID),
			agent.WithWorkspaceRoot(request.WorkspaceRoot),
			agent.WithPolicyIdentity(cloneIdentity(request.Identity)),
			agent.WithContextIdentity(request.TenantID, request.WorkspaceID, request.ProjectID, entry.record.ID, request.RequestID),
			agent.WithApprovalResolution(request.Approval),
		)
		if request.Model.ID != "" {
			options = append(options, agent.WithModel(request.Model))
		}
		if request.SystemPrompt != "" {
			options = append(options, agent.WithSystemPrompt(request.SystemPrompt))
		}
		if request.PolicyVersion != "" {
			options = append(options, agent.WithPolicyVersion(request.PolicyVersion))
		}
		if !request.AuthorizationExpiresAt.IsZero() {
			options = append(options, agent.WithAuthorizationExpiry(request.AuthorizationExpiresAt))
		}
		childAgent = agent.New(provider, registry, options...)
		m.mu.Lock()
		entry.runtime = childAgent
		m.mu.Unlock()
	}
	if cancelRequested {
		childAgent.Abort()
	}
	result, err := childAgent.Run(ctx, request.Prompt)
	return summarize(result, err, m.cfg.MaxResultBytes), err
}

func (m *Manager) validateParentLocked(parentID string) (int, error) {
	parent, err := m.store.Load(context.Background(), parentID)
	if errors.Is(err, ErrRecordNotFound) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	if !m.cfg.AllowNested {
		return 0, ErrNestedDisabled
	}
	return parent.Depth + 1, nil
}

func (m *Manager) childCountLocked(ctx context.Context, parentID string) (int, error) {
	count := 0
	for _, entry := range m.jobs {
		if entry.record.ParentID == parentID {
			count++
		}
	}
	records, err := m.store.List(ctx, parentID)
	if err != nil {
		return 0, err
	}
	if len(records) > count {
		count = len(records)
	}
	return count, nil
}

func (m *Manager) loadOwned(ctx context.Context, parentID, id string) (Record, error) {
	if m == nil {
		return Record{}, ErrManagerClosed
	}
	parentID = strings.TrimSpace(parentID)
	id = strings.TrimSpace(id)
	if parentID == "" || id == "" {
		return Record{}, fmt.Errorf("%w: parent ID and child ID are required", ErrInvalidRequest)
	}
	m.mu.RLock()
	if entry, ok := m.jobs[id]; ok {
		record := cloneRecord(entry.record)
		m.mu.RUnlock()
		if record.ParentID != parentID {
			return Record{}, ErrNotOwned
		}
		return record, nil
	}
	m.mu.RUnlock()
	record, err := m.store.Load(ctx, id)
	if errors.Is(err, ErrRecordNotFound) {
		return Record{}, ErrRecordNotFound
	}
	if err != nil {
		return Record{}, err
	}
	if record.ParentID != parentID {
		return Record{}, ErrNotOwned
	}
	return record, nil
}

func (m *Manager) saveLocked(entry *job) error {
	if entry.record.Version == 0 {
		return errors.New("subagent record has no version")
	}
	if err := m.store.Save(context.Background(), entry.record.ID, entry.record.Version, entry.record); err != nil {
		return err
	}
	entry.record.Version++
	return nil
}

// RetryPersistence retries a terminal state update that failed after the
// child execution itself had already completed. The child remains observable
// in memory until this operation succeeds.
func (m *Manager) RetryPersistence(ctx context.Context, parentID, id string) error {
	if m == nil {
		return ErrManagerClosed
	}
	ctx = nonNilContext(ctx)
	if err := contextErr(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	entry, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	if entry.record.ParentID != parentID {
		m.mu.Unlock()
		return ErrNotOwned
	}
	if entry.persistenceErr == nil {
		m.mu.Unlock()
		return nil
	}
	if !isTerminal(entry.record.Status) {
		m.mu.Unlock()
		return fmt.Errorf("%w: child %q is not terminal", ErrInvalidRequest, id)
	}
	record := cloneRecord(entry.record)
	record.Error = record.Result.Error
	if err := m.store.Save(ctx, record.ID, record.Version, record); err != nil {
		entry.persistenceErr = err
		entry.record.Error = persistenceError(err).Error()
		m.mu.Unlock()
		return err
	}
	record.Version++
	entry.record = record
	entry.persistenceErr = nil
	delete(m.jobs, id)
	m.signalLocked()
	m.mu.Unlock()
	return nil
}

func (m *Manager) finishLocked(entry *job, status Status, result Result) {
	if isTerminal(entry.record.Status) {
		return
	}
	entry.record.Status = status
	entry.record.Result = cloneResult(result)
	entry.record.Error = result.Error
	if isTerminal(status) {
		entry.record.FinishedAt = time.Now().UTC()
	} else {
		entry.record.FinishedAt = time.Time{}
	}
	entry.persistenceErr = nil
	if err := m.saveLocked(entry); err != nil {
		entry.persistenceErr = err
		entry.record.Error = persistenceError(err).Error()
	}
	if status == StatusWaitingApproval {
		m.signalLocked()
		return
	}
	entry.runtime = nil
	if entry.cancel != nil {
		entry.cancel()
	}
	if entry.persistenceErr == nil {
		delete(m.jobs, entry.record.ID)
	}
	close(entry.done)
	m.signalLocked()
}

func persistenceError(err error) error {
	return fmt.Errorf("%w: %w", ErrPersistence, err)
}

func (m *Manager) signalLocked() {
	close(m.changed)
	m.changed = make(chan struct{})
}

func (m *Manager) consumeFollowUp(parentID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.followUps[parentID] {
		return false
	}
	delete(m.followUps, parentID)
	return true
}

func statusForResult(ctx context.Context, entry *job, result Result, err error) Status {
	if entry.cancelRequested || result.State == agent.StateCanceled || errors.Is(ctx.Err(), context.Canceled) ||
		errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return StatusCanceled
	}
	if result.State == agent.StateWaitingApproval || errors.Is(err, agent.ErrApprovalPending) {
		return StatusWaitingApproval
	}
	if err != nil || result.State == agent.StateFailed {
		return StatusFailed
	}
	return StatusCompleted
}

func stateForStatus(status Status) agent.State {
	switch status {
	case StatusCanceled:
		return agent.StateCanceled
	case StatusFailed:
		return agent.StateFailed
	case StatusWaitingApproval:
		return agent.StateWaitingApproval
	default:
		return agent.StateCompleted
	}
}

func isTerminal(status Status) bool {
	return status == StatusCompleted || status == StatusFailed || status == StatusCanceled
}

func summarize(result agent.RunResult, runErr error, maxBytes int) Result {
	output := Result{
		State:      result.State,
		Turns:      result.Turns,
		StopReason: result.StopReason,
		Usage:      result.Usage,
		Approval:   cloneApproval(result.Approval),
	}
	if runErr != nil {
		output.Error = runErr.Error()
	}
	if result.Error != nil && output.Error == "" {
		output.Error = result.Error.Message
	}
	for i := len(result.Messages) - 1; i >= 0; i-- {
		message := result.Messages[i]
		if message.Role != ai.RoleAssistant {
			continue
		}
		var builder strings.Builder
		for _, block := range message.Content {
			if block.Text != "" {
				builder.WriteString(block.Text)
			}
		}
		output.Text = builder.String()
		break
	}
	if maxBytes > 0 && len(output.Text) > maxBytes {
		output.Text = output.Text[:maxBytes]
		output.Truncated = true
	}
	return output
}

func newID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate subagent ID: %w", err)
	}
	return "subagent-" + hex.EncodeToString(raw[:]), nil
}

func cloneIdentity(identity policy.Identity) policy.Identity {
	identity.Capabilities = append([]string(nil), identity.Capabilities...)
	return identity
}

func childIdentity(identity policy.Identity, childID string) policy.Identity {
	identity = cloneIdentity(identity)
	identity.RunID = childID
	identity.SessionID = childID
	return identity
}

func cloneRequest(request SpawnRequest) SpawnRequest {
	request.Identity = cloneIdentity(request.Identity)
	request.Approval = cloneApprovalResolution(request.Approval)
	request.Model.Input = append([]ai.InputKind(nil), request.Model.Input...)
	request.Model.Headers = cloneStringMap(request.Model.Headers)
	request.Model.Metadata = append([]byte(nil), request.Model.Metadata...)
	return request
}

func cloneRecord(record Record) Record {
	record.Request = cloneRequest(record.Request)
	record.Result = cloneResult(record.Result)
	return record
}

func cloneResult(result Result) Result {
	result.Approval = cloneApproval(result.Approval)
	return result
}

func cloneApprovalResolution(resolution *policy.ApprovalResolution) *policy.ApprovalResolution {
	if resolution == nil {
		return nil
	}
	copy := *resolution
	return &copy
}

func cloneApproval(approval *policy.ApprovalRequest) *policy.ApprovalRequest {
	if approval == nil {
		return nil
	}
	copy := *approval
	return &copy
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}
