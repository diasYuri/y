package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	policypkg "github.com/yuri/y/internal/policy"
	"github.com/yuri/y/internal/telemetry"
)

type registryEntry struct {
	desc    ToolDescriptor
	handler ToolHandler
}

// ApprovalHandler resolves approval requests surfaced by the registry.
type ApprovalHandler interface {
	RequestApproval(context.Context, policypkg.ApprovalRequest) (*policypkg.ApprovalResolution, error)
}

// ApprovalHandlerFunc adapts a function to ApprovalHandler.
type ApprovalHandlerFunc func(context.Context, policypkg.ApprovalRequest) (*policypkg.ApprovalResolution, error)

// RequestApproval calls f.
func (f ApprovalHandlerFunc) RequestApproval(ctx context.Context, req policypkg.ApprovalRequest) (*policypkg.ApprovalResolution, error) {
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
}

// NewRegistry creates an empty tool registry.
func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{entries: make(map[string]registryEntry)}
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
	desc, handler, ok := r.Get(req.Name)
	if !ok {
		return ToolResponse{}, toolError("tool_not_found", fmt.Sprintf("tool %q not found", req.Name), ErrToolNotFound)
	}
	policy := r.policy
	if policy == nil {
		policy = WorkspacePolicy()
	}
	if desc.Sensitive {
		decision, err := decide(ctx, policy, PolicyRequest{
			ToolName:      req.Name,
			Capability:    capabilityForPolicy(desc),
			WorkspaceRoot: req.WorkspaceRoot,
			Path:          req.Name,
			Sensitive:     true,
			Approval:      req.Approval,
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
				return ToolResponse{}, toolError("approval_required", message, ErrApprovalRequired)
			}
			approval, err := r.approvalHandler.RequestApproval(ctx, *decision.Approval)
			if err != nil {
				return ToolResponse{}, err
			}
			if approval == nil {
				return ToolResponse{}, toolError("approval_required", fmt.Sprintf("tool %q approval cancelled", req.Name), ErrApprovalRequired)
			}
			req.Approval = approval
			if err := authorize(ctx, policy, PolicyRequest{
				ToolName:      req.Name,
				Capability:    capabilityForPolicy(desc),
				WorkspaceRoot: req.WorkspaceRoot,
				Path:          req.Name,
				Sensitive:     true,
				Approval:      req.Approval,
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

	start := time.Now()
	resp, err := handler.Handle(ctx, req)
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
		IsError: resp.IsError,
		Details: append([]byte(nil), resp.Details...),
	}
	for _, c := range resp.Content {
		cloned.Content = append(cloned.Content, ContentBlock{
			Type: c.Type,
			Text: c.Text,
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
