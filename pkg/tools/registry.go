package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/diasYuri/y/pkg/policy"
	"github.com/diasYuri/y/pkg/telemetry"
)

type registryEntry struct {
	desc         ToolDescriptor
	handler      ToolHandler
	registration *ToolRegistration
	owner        string
	generation   uint64
}

type scopedToolKey struct {
	name       string
	owner      string
	generation uint64
}

// ApprovalHandler resolves approval requests surfaced by the registry.
type ApprovalHandler interface {
	RequestApproval(context.Context, policy.ApprovalRequest) (*policy.ApprovalResolution, error)
}

// ApprovalHandlerFunc adapts a function to ApprovalHandler.
type ApprovalHandlerFunc func(context.Context, policy.ApprovalRequest) (*policy.ApprovalResolution, error)

// RequestApproval calls f.
func (f ApprovalHandlerFunc) RequestApproval(ctx context.Context, req policy.ApprovalRequest) (*policy.ApprovalResolution, error) {
	return f(ctx, req)
}

// RegistryOption configures a Registry.
type RegistryOption func(*Registry)

// ToolExecutionNext continues execution after authorization and schema checks.
type ToolExecutionNext func(context.Context, ToolRequest) (ToolResponse, error)

// ToolExecutionMiddleware wraps validated tool execution. Middleware can return
// a durable replay response without invoking next.
type ToolExecutionMiddleware func(context.Context, ToolRequest, ToolDescriptor, ToolExecutionNext) (ToolResponse, error)

// CacheKey identifies a cached tool call.
type cacheKey struct {
	name          string
	arguments     string
	workspaceRoot string
}

// cachedResult stores a tool response with its expiration time.
type cachedResult struct {
	resp      ToolResponse
	expiresAt time.Time
}

type inflightOperation struct {
	done chan struct{}
	resp ToolResponse
	err  error
}

// Registry stores tool descriptors and handlers.
type Registry struct {
	mu                   sync.RWMutex
	entries              map[string]registryEntry
	scopedEntries        map[scopedToolKey]registryEntry
	scopedOwners         map[string]string
	activeOwners         map[string]uint64
	revision             uint64
	registrations        map[*ToolRegistration]struct{}
	policy               Policy
	approvalHandler      ApprovalHandler
	teleEmitter          telemetry.Emitter
	executionMiddlewares []ToolExecutionMiddleware
	cache                map[cacheKey]cachedResult
	cacheEnabled         bool
	cacheTTL             time.Duration
	cacheMaxSize         int
	operations           map[string]ToolResponse
	inflight             map[string]*inflightOperation
	closers              []func(context.Context) error
	closeOnce            sync.Once
	closeDone            chan struct{}
	closeErr             error
	closed               bool
}

func capabilityNames(capabilities []Capability) []string {
	if len(capabilities) == 0 {
		return nil
	}
	out := make([]string, len(capabilities))
	for i, capability := range capabilities {
		out[i] = string(capability)
	}
	return out
}

// NewRegistry creates an empty tool registry.
func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{entries: make(map[string]registryEntry), scopedEntries: make(map[scopedToolKey]registryEntry), scopedOwners: make(map[string]string), activeOwners: make(map[string]uint64), registrations: make(map[*ToolRegistration]struct{}), operations: make(map[string]ToolResponse), inflight: make(map[string]*inflightOperation), closeDone: make(chan struct{})}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// AddCloser registers a resource owned by this registry. Closers run once,
// in reverse registration order, when Close is called. Every closer is
// attempted; Close returns all cleanup errors joined together.
func (r *Registry) AddCloser(closer func(context.Context) error) error {
	if r == nil || closer == nil {
		return toolError("invalid_tool", "registry closer is nil", ErrInvalidTool)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return toolError("registry_closed", "tool registry is closed", ErrInvalidTool)
	}
	r.closers = append(r.closers, closer)
	return nil
}

// Close shuts down registry-owned resources and joins every cleanup error. A
// timed-out caller may call Close again to wait for the shutdown to finish.
func (r *Registry) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		registrations := make([]*ToolRegistration, 0, len(r.registrations))
		for registration := range r.registrations {
			registrations = append(registrations, registration)
			registration.gate.retire()
		}
		r.entries = make(map[string]registryEntry)
		r.scopedEntries = make(map[scopedToolKey]registryEntry)
		closers := append([]func(context.Context) error(nil), r.closers...)
		r.mu.Unlock()
		go func() {
			defer close(r.closeDone)
			var closeErr error
			for _, registration := range registrations {
				<-registration.gate.done
			}
			for i := len(closers) - 1; i >= 0; i-- {
				if err := closers[i](context.Background()); err != nil {
					closeErr = errors.Join(closeErr, err)
				}
			}
			r.mu.Lock()
			r.registrations = make(map[*ToolRegistration]struct{})
			r.closeErr = closeErr
			r.mu.Unlock()
		}()
	})
	select {
	case <-r.closeDone:
		r.mu.RLock()
		err := r.closeErr
		r.mu.RUnlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WithPolicy configures the registry to apply policy checks before sensitive tools run.
func WithPolicy(policy Policy) RegistryOption {
	return func(r *Registry) {
		r.policy = policy
	}
}

// AddExecutionMiddleware adds a registry-lifetime wrapper that runs after
// policy authorization and argument validation, immediately before execution.
func (r *Registry) AddExecutionMiddleware(middleware ToolExecutionMiddleware) error {
	if r == nil || middleware == nil {
		return toolError("invalid_middleware", "execution middleware is nil", ErrInvalidTool)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return toolError("registry_closed", "cannot add middleware to a closed registry", ErrInvalidTool)
	}
	r.executionMiddlewares = append(r.executionMiddlewares, middleware)
	return nil
}

// WithApprovalHandler configures how approval requests are surfaced.
func WithApprovalHandler(handler ApprovalHandler) RegistryOption {
	return func(r *Registry) {
		r.approvalHandler = handler
	}
}

// WithTelemetryEmitter configures the telemetry emitter for tool calls.
func WithTelemetryEmitter(emitter telemetry.Emitter) RegistryOption {
	return func(r *Registry) {
		r.teleEmitter = emitter
	}
}

// WithCache enables tool result caching with the given TTL and max size.
func WithCache(ttl time.Duration, maxSize int) RegistryOption {
	return func(r *Registry) {
		r.cacheEnabled = true
		r.cacheTTL = ttl
		r.cacheMaxSize = maxSize
		r.cache = make(map[cacheKey]cachedResult)
	}
}

// Add registers a tool descriptor and handler.
func (r *Registry) Add(desc ToolDescriptor, handler ToolHandler) error {
	_, err := r.register(desc, handler)
	return err
}

// Register adds a tool and returns a handle that removes it and drains calls
// already dispatched to it. Use Add for registry-lifetime registrations.
func (r *Registry) Register(desc ToolDescriptor, handler ToolHandler) (*ToolRegistration, error) {
	return r.register(desc, handler)
}

func (r *Registry) register(desc ToolDescriptor, handler ToolHandler) (*ToolRegistration, error) {
	return r.registerOwned(desc, handler, "", 0)
}

// RegisterScoped adds a tool owned by one component generation. Scoped entries
// with the same name may coexist across generations so old snapshots can finish.
func (r *Registry) RegisterScoped(desc ToolDescriptor, handler ToolHandler, owner string, generation uint64) (*ToolRegistration, error) {
	if owner == "" || generation == 0 {
		return nil, toolError("invalid_tool", "scoped registration requires an owner and generation", ErrInvalidTool)
	}
	return r.registerOwned(desc, handler, owner, generation)
}

func (r *Registry) registerOwned(desc ToolDescriptor, handler ToolHandler, owner string, generation uint64) (*ToolRegistration, error) {
	if desc.Name == "" {
		return nil, toolError("invalid_tool", "tool descriptor has empty name", ErrInvalidTool)
	}
	if handler == nil {
		return nil, toolError("invalid_tool", fmt.Sprintf("tool %q has nil handler", desc.Name), ErrInvalidTool)
	}
	if r == nil {
		return nil, toolError("invalid_tool", "registry is nil", ErrInvalidTool)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, toolError("registry_closed", "tool registry is closed", ErrInvalidTool)
	}
	if r.entries == nil {
		r.entries = make(map[string]registryEntry)
	}
	if r.scopedEntries == nil {
		r.scopedEntries = make(map[scopedToolKey]registryEntry)
	}
	if r.registrations == nil {
		r.registrations = make(map[*ToolRegistration]struct{})
	}
	if owner == "" {
		if _, exists := r.entries[desc.Name]; exists || r.scopedOwners[desc.Name] != "" {
			return nil, toolError("duplicate_tool", fmt.Sprintf("tool %q already registered", desc.Name), ErrToolAlreadyRegistered)
		}
	} else {
		if _, exists := r.entries[desc.Name]; exists {
			return nil, toolError("duplicate_tool", fmt.Sprintf("tool %q already registered", desc.Name), ErrToolAlreadyRegistered)
		}
		if existingOwner := r.scopedOwners[desc.Name]; existingOwner != "" && existingOwner != owner {
			return nil, toolError("duplicate_tool", fmt.Sprintf("tool %q is owned by component %q", desc.Name, existingOwner), ErrToolAlreadyRegistered)
		}
		key := scopedToolKey{name: desc.Name, owner: owner, generation: generation}
		if _, exists := r.scopedEntries[key]; exists {
			return nil, toolError("duplicate_tool", fmt.Sprintf("tool %q already registered by %q generation %d", desc.Name, owner, generation), ErrToolAlreadyRegistered)
		}
	}
	registration := &ToolRegistration{registry: r, name: desc.Name, owner: owner, generation: generation, gate: newRegistrationGate()}
	entry := registryEntry{desc: cloneDescriptor(desc), handler: handler, registration: registration, owner: owner, generation: generation}
	if owner == "" {
		r.entries[desc.Name] = entry
	} else {
		r.scopedEntries[scopedToolKey{name: desc.Name, owner: owner, generation: generation}] = entry
		r.scopedOwners[desc.Name] = owner
	}
	r.registrations[registration] = struct{}{}
	r.revision++
	return registration, nil
}

// Get returns a registered descriptor and handler.
func (r *Registry) Get(name string) (ToolDescriptor, ToolHandler, bool) {
	if r == nil {
		return ToolDescriptor{}, nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entryForOwnersLocked(name, r.activeOwners)
	if !ok {
		return ToolDescriptor{}, nil, false
	}
	return cloneDescriptor(entry.desc), entry.handler, true
}

// List returns registered descriptors in stable name order.
func (r *Registry) List() []ToolDescriptor {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	selected := r.entriesForOwnersLocked(r.activeOwners)
	out := make([]ToolDescriptor, 0, len(selected))
	for _, entry := range selected {
		out = append(out, cloneDescriptor(entry.desc))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GetExecutionMode returns the execution mode for a registered tool.
func (r *Registry) GetExecutionMode(name string) ExecutionMode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entryForOwnersLocked(name, r.activeOwners)
	if !ok {
		return ExecutionParallel
	}
	if entry.desc.ExecutionMode == "" {
		return ExecutionParallel
	}
	return entry.desc.ExecutionMode
}

// Handle executes a registered tool by request name.
func (r *Registry) Handle(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	desc, handler, gate, closed, ok := r.acquire(req.Name)
	if closed {
		return ToolResponse{}, toolError("registry_closed", "tool registry is closed", ErrInvalidTool)
	}
	if !ok {
		return ToolResponse{}, toolError("tool_not_found", fmt.Sprintf("tool %q not found", req.Name), ErrToolNotFound)
	}
	defer gate.release()
	return r.handleCaptured(ctx, req, desc, handler)
}

func (r *Registry) handleCaptured(ctx context.Context, req ToolRequest, desc ToolDescriptor, handler ToolHandler) (ToolResponse, error) {
	policyEngine := r.policy
	if policyEngine == nil {
		policyEngine = WorkspacePolicy()
	}
	auditAll := false
	if marker, ok := policyEngine.(interface{ AuditAllTools() bool }); ok {
		auditAll = marker.AuditAllTools()
	}
	if desc.Sensitive || len(desc.Capabilities) > 0 || auditAll {
		decision, err := decide(ctx, policyEngine, PolicyRequest{
			ToolName:               req.Name,
			Capability:             capabilityForPolicy(desc),
			WorkspaceRoot:          req.WorkspaceRoot,
			Path:                   req.Name,
			Sensitive:              desc.Sensitive,
			Approval:               req.Approval,
			Identity:               req.Identity,
			RequestID:              req.RequestID,
			RunID:                  req.RunID,
			TurnID:                 req.TurnID,
			ToolCallID:             req.ID,
			Arguments:              req.Arguments,
			PolicyVersion:          req.PolicyVersion,
			AuthorizationExpiresAt: req.AuthorizationExpiresAt,
			RequiredCapabilities:   capabilityNames(desc.Capabilities),
		})
		if err != nil {
			return ToolResponse{}, err
		}
		if decision.Kind == DecisionRequireApproval && req.Approval == nil {
			if r.approvalHandler == nil || decision.Approval == nil {
				message := fmt.Sprintf("tool %q requires approval", req.Name)
				if decision.Reason != "" {
					message += ": " + decision.Reason
				}
				approval := policy.ApprovalRequest{ToolName: req.Name, RequestID: req.RequestID, RunID: req.RunID, TurnID: req.TurnID, ToolCallID: req.ID, Reason: message}
				if decision.Approval != nil {
					approval = *decision.Approval
				}
				return ToolResponse{}, &ApprovalError{Request: approval, Cause: ErrApprovalRequired}
			}
			approval, err := r.approvalHandler.RequestApproval(ctx, *decision.Approval)
			if err != nil {
				return ToolResponse{}, err
			}
			if approval == nil {
				return ToolResponse{}, &ApprovalError{Request: *decision.Approval, Cause: ErrApprovalRequired}
			}
			if approval.ApprovalID == "" {
				approval.ApprovalID = decision.Approval.ApprovalID
			}
			req.Approval = approval
			if err := authorize(ctx, policyEngine, PolicyRequest{
				ToolName:               req.Name,
				Capability:             capabilityForPolicy(desc),
				WorkspaceRoot:          req.WorkspaceRoot,
				Path:                   req.Name,
				Sensitive:              desc.Sensitive,
				Approval:               req.Approval,
				Identity:               req.Identity,
				RequestID:              req.RequestID,
				RunID:                  req.RunID,
				TurnID:                 req.TurnID,
				ToolCallID:             req.ID,
				Arguments:              req.Arguments,
				PolicyVersion:          req.PolicyVersion,
				AuthorizationExpiresAt: req.AuthorizationExpiresAt,
				RequiredCapabilities:   capabilityNames(desc.Capabilities),
			}); err != nil {
				return ToolResponse{}, err
			}
		} else if decision.Kind != DecisionAllow {
			message := fmt.Sprintf("tool %q denied by policy", req.Name)
			if decision.Reason != "" {
				message += ": " + decision.Reason
			}
			return ToolResponse{}, toolError("policy_denied", message, ErrPolicyDenied)
		}
	}

	// Validate arguments against schema.
	if len(desc.InputSchema) > 0 {
		if err := ValidateArguments(req.Arguments, desc.InputSchema); err != nil {
			return ToolResponse{}, err
		}
	}

	return r.runExecutionMiddleware(ctx, req, desc, handler)
}

func (r *Registry) runExecutionMiddleware(ctx context.Context, req ToolRequest, desc ToolDescriptor, handler ToolHandler) (ToolResponse, error) {
	r.mu.RLock()
	middlewares := append([]ToolExecutionMiddleware(nil), r.executionMiddlewares...)
	r.mu.RUnlock()
	next := ToolExecutionNext(func(nextCtx context.Context, nextReq ToolRequest) (ToolResponse, error) {
		return r.executeValidated(nextCtx, nextReq, desc, handler, len(middlewares) > 0)
	})
	for i := len(middlewares) - 1; i >= 0; i-- {
		middleware := middlewares[i]
		downstream := next
		next = func(nextCtx context.Context, nextReq ToolRequest) (ToolResponse, error) {
			return middleware(nextCtx, nextReq, desc, downstream)
		}
	}
	return next(ctx, req)
}

func (r *Registry) executeValidated(ctx context.Context, req ToolRequest, desc ToolDescriptor, handler ToolHandler, skipCache bool) (ToolResponse, error) {
	// A durable execution journal is authoritative over process-local cached results.
	key := cacheKey{name: req.Name, arguments: string(req.Arguments), workspaceRoot: req.WorkspaceRoot}
	if !skipCache && r.cacheEnabled && !desc.Sensitive {
		r.mu.RLock()
		cached, ok := r.cache[key]
		r.mu.RUnlock()
		if ok && time.Now().Before(cached.expiresAt) {
			return cloneResponse(cached.resp), nil
		}
	}

	// Tool call IDs are the idempotency boundary for side effects. Reserve the
	// operation only after validation and the read-only result cache check so a
	// malformed request never blocks a valid retry. Concurrent duplicates wait
	// for the first handler and receive the same result.
	if req.IdempotencyKey != "" {
		r.mu.Lock()
		completed, ok := r.operations[req.IdempotencyKey]
		if ok {
			r.mu.Unlock()
			return cloneResponse(completed), nil
		}
		if operation := r.inflight[req.IdempotencyKey]; operation != nil {
			r.mu.Unlock()
			select {
			case <-operation.done:
				return cloneResponse(operation.resp), operation.err
			case <-ctx.Done():
				return ToolResponse{}, ctx.Err()
			}
		}
		if r.inflight == nil {
			r.inflight = make(map[string]*inflightOperation)
		}
		operation := &inflightOperation{done: make(chan struct{})}
		r.inflight[req.IdempotencyKey] = operation
		r.mu.Unlock()

		resp, err := r.executeHandler(ctx, handler, req, desc, skipCache)
		r.completeOperation(req.IdempotencyKey, operation, resp, err)
		return resp, err
	}

	return r.executeHandler(ctx, handler, req, desc, skipCache)
}

func (r *Registry) acquire(name string) (ToolDescriptor, ToolHandler, *registrationGate, bool, bool) {
	if r == nil {
		return ToolDescriptor{}, nil, nil, false, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return ToolDescriptor{}, nil, nil, true, false
	}
	entry, ok := r.entryForOwnersLocked(name, r.activeOwners)
	if !ok || !entry.registration.gate.acquire() {
		return ToolDescriptor{}, nil, nil, false, false
	}
	return cloneDescriptor(entry.desc), entry.handler, entry.registration.gate, false, true
}

func (r *Registry) entryForOwnersLocked(name string, owners map[string]uint64) (registryEntry, bool) {
	if entry, ok := r.entries[name]; ok {
		return entry, true
	}
	for key, entry := range r.scopedEntries {
		if key.name == name && owners[key.owner] == key.generation {
			return entry, true
		}
	}
	return registryEntry{}, false
}

func (r *Registry) entriesForOwnersLocked(owners map[string]uint64) map[string]registryEntry {
	entries := make(map[string]registryEntry, len(r.entries)+len(r.scopedEntries))
	for name, entry := range r.entries {
		entries[name] = entry
	}
	for key, entry := range r.scopedEntries {
		if owners[key.owner] == key.generation {
			if _, exists := entries[key.name]; !exists {
				entries[key.name] = entry
			}
		}
	}
	return entries
}

// PublishOwners updates the live, non-snapshot registry view after a manager
// commit. Existing snapshots continue to dispatch their captured entries.
func (r *Registry) PublishOwners(owners map[string]uint64) {
	if r == nil {
		return
	}
	copy := make(map[string]uint64, len(owners))
	for id, generation := range owners {
		copy[id] = generation
	}
	r.mu.Lock()
	r.activeOwners = copy
	r.mu.Unlock()
}

func (r *Registry) refreshToolOwnerLocked(name string) {
	for key := range r.scopedEntries {
		if key.name == name {
			r.scopedOwners[name] = key.owner
			return
		}
	}
	delete(r.scopedOwners, name)
}

// authorizeOperation applies the registry's policy and approval handler to a
// nested operation such as starting a persistent language-server process.
func (r *Registry) authorizeOperation(ctx context.Context, req PolicyRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	req = policyRequestWithToolContext(ctx, req)
	r.mu.RLock()
	engine := r.policy
	approvalHandler := r.approvalHandler
	r.mu.RUnlock()
	decision, err := decide(ctx, engine, req)
	if err != nil {
		return err
	}
	if decision.Kind == DecisionAllow {
		return nil
	}
	if decision.Kind == DecisionRequireApproval {
		if decision.Approval == nil {
			return toolError("approval_required", "process execution requires approval", ErrApprovalRequired)
		}
		if approvalHandler == nil {
			return &ApprovalError{Request: *decision.Approval, Cause: ErrApprovalRequired}
		}
		approval, err := approvalHandler.RequestApproval(ctx, *decision.Approval)
		if err != nil {
			return err
		}
		req.Approval = approval
		decision, err = decide(ctx, engine, req)
		if err != nil {
			return err
		}
	}
	if decision.Kind != DecisionAllow {
		reason := decision.Reason
		if reason == "" {
			reason = "process execution denied by policy"
		}
		return toolError("policy_denied", reason, ErrPolicyDenied)
	}
	return nil
}

func (r *Registry) executeHandler(ctx context.Context, handler ToolHandler, req ToolRequest, desc ToolDescriptor, skipCache bool) (ToolResponse, error) {
	start := time.Now()
	resp, err := handler.Handle(withToolRequestContext(ctx, req), req)
	durationMs := time.Since(start).Milliseconds()

	if r.teleEmitter != nil {
		errStr := ""
		if err != nil {
			errStr = err.Error()
		}
		r.teleEmitter.Emit(telemetry.NewEvent(
			telemetry.EventToolCall,
			"",
			telemetry.ToolCallPayload(req.Name, durationMs, errStr),
		))
	}

	if !skipCache && r.cacheEnabled && !desc.Sensitive && err == nil {
		key := cacheKey{name: req.Name, arguments: string(req.Arguments), workspaceRoot: req.WorkspaceRoot}
		r.mu.Lock()
		r.evictIfNeeded()
		r.cache[key] = cachedResult{resp: cloneResponse(resp), expiresAt: time.Now().Add(r.cacheTTL)}
		r.mu.Unlock()
	}
	return resp, err
}

func (r *Registry) completeOperation(key string, operation *inflightOperation, resp ToolResponse, err error) {
	r.mu.Lock()
	if err == nil {
		if r.operations == nil {
			r.operations = make(map[string]ToolResponse)
		}
		r.operations[key] = cloneResponse(resp)
	}
	operation.resp = cloneResponse(resp)
	operation.err = err
	delete(r.inflight, key)
	close(operation.done)
	r.mu.Unlock()
}

func (r *Registry) evictIfNeeded() {
	if len(r.cache) < r.cacheMaxSize {
		return
	}
	now := time.Now()
	// First pass: remove expired entries.
	for k, v := range r.cache {
		if now.After(v.expiresAt) {
			delete(r.cache, k)
		}
	}
	// Second pass: if still over limit, remove oldest half.
	if len(r.cache) >= r.cacheMaxSize {
		var keys []cacheKey
		for k := range r.cache {
			keys = append(keys, k)
		}
		half := len(keys) / 2
		for i := 0; i < half && i < len(keys); i++ {
			delete(r.cache, keys[i])
		}
	}
}

func cloneResponse(resp ToolResponse) ToolResponse {
	cloned := ToolResponse{
		IsError:  resp.IsError,
		Details:  append([]byte(nil), resp.Details...),
		Metadata: append([]byte(nil), resp.Metadata...),
		Logs:     append([]LogEntry(nil), resp.Logs...),
	}
	for i := range cloned.Logs {
		cloned.Logs[i].Details = append([]byte(nil), cloned.Logs[i].Details...)
	}
	for _, c := range resp.Content {
		cloned.Content = append(cloned.Content, ContentBlock{
			Type:             c.Type,
			Text:             c.Text,
			ImageData:        append([]byte(nil), c.ImageData...),
			ImageMIMEType:    c.ImageMIMEType,
			Details:          append([]byte(nil), c.Details...),
			ProviderMetadata: append([]byte(nil), c.ProviderMetadata...),
		})
	}
	return cloned
}

func cloneDescriptor(desc ToolDescriptor) ToolDescriptor {
	desc.InputSchema = append([]byte(nil), desc.InputSchema...)
	desc.Capabilities = append([]Capability(nil), desc.Capabilities...)
	return desc
}

func capabilityForPolicy(desc ToolDescriptor) string {
	if len(desc.Capabilities) == 0 {
		return ""
	}
	return string(desc.Capabilities[0])
}
