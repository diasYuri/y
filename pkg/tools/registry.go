package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/yuri/y/pkg/policy"
	"github.com/yuri/y/pkg/telemetry"
)

type registryEntry struct {
	desc    ToolDescriptor
	handler ToolHandler
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
	mu              sync.RWMutex
	entries         map[string]registryEntry
	policy          Policy
	approvalHandler ApprovalHandler
	teleEmitter     telemetry.Emitter
	cache           map[cacheKey]cachedResult
	cacheEnabled    bool
	cacheTTL        time.Duration
	cacheMaxSize    int
	operations      map[string]ToolResponse
	inflight        map[string]*inflightOperation
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
	r := &Registry{entries: make(map[string]registryEntry), operations: make(map[string]ToolResponse), inflight: make(map[string]*inflightOperation)}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// WithPolicy configures the registry to apply policy checks before sensitive tools run.
func WithPolicy(policy Policy) RegistryOption {
	return func(r *Registry) {
		r.policy = policy
	}
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
	if desc.Name == "" {
		return toolError("invalid_tool", "tool descriptor has empty name", ErrInvalidTool)
	}
	if handler == nil {
		return toolError("invalid_tool", fmt.Sprintf("tool %q has nil handler", desc.Name), ErrInvalidTool)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = make(map[string]registryEntry)
	}
	if _, exists := r.entries[desc.Name]; exists {
		return toolError("duplicate_tool", fmt.Sprintf("tool %q already registered", desc.Name), ErrToolAlreadyRegistered)
	}
	r.entries[desc.Name] = registryEntry{desc: cloneDescriptor(desc), handler: handler}
	return nil
}

// Get returns a registered descriptor and handler.
func (r *Registry) Get(name string) (ToolDescriptor, ToolHandler, bool) {
	if r == nil {
		return ToolDescriptor{}, nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[name]
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
	out := make([]ToolDescriptor, 0, len(r.entries))
	for _, entry := range r.entries {
		out = append(out, cloneDescriptor(entry.desc))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GetExecutionMode returns the execution mode for a registered tool.
func (r *Registry) GetExecutionMode(name string) ExecutionMode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[name]
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
	desc, handler, ok := r.Get(req.Name)
	if !ok {
		return ToolResponse{}, toolError("tool_not_found", fmt.Sprintf("tool %q not found", req.Name), ErrToolNotFound)
	}
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

	// Check cache for non-sensitive, non-mutating tools.
	key := cacheKey{name: req.Name, arguments: string(req.Arguments), workspaceRoot: req.WorkspaceRoot}
	if r.cacheEnabled && !desc.Sensitive {
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

		resp, err := r.executeHandler(ctx, handler, req, desc)
		r.completeOperation(req.IdempotencyKey, operation, resp, err)
		return resp, err
	}

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

	// Store successful result in cache.
	if r.cacheEnabled && !desc.Sensitive && err == nil {
		r.mu.Lock()
		r.evictIfNeeded()
		r.cache[key] = cachedResult{resp: cloneResponse(resp), expiresAt: time.Now().Add(r.cacheTTL)}
		r.mu.Unlock()
	}
	return resp, err
}

func (r *Registry) executeHandler(ctx context.Context, handler ToolHandler, req ToolRequest, desc ToolDescriptor) (ToolResponse, error) {
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

	if r.cacheEnabled && !desc.Sensitive && err == nil {
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
