package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/diasYuri/y/pkg/agent/compaction"
	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/providers"
)

// RuntimeHookRegistry stores removable runtime-hook registrations. Agents that
// use the registry see additions and removals on subsequent hook dispatches.
type RuntimeHookRegistry struct {
	mu            sync.RWMutex
	registrations []*HookRegistration
	activeOwners  map[string]uint64
	revision      uint64
}

// HookRegistration owns one RuntimeHooks value and drains callbacks already in
// progress when closed.
type HookRegistration struct {
	registry   *RuntimeHookRegistry
	hooks      RuntimeHooks
	owner      string
	generation uint64
	gate       hookGate
}

type hookGate struct {
	mu        sync.Mutex
	active    bool
	inflight  int
	snapshots int
	done      chan struct{}
	finished  bool
}

func newHookGate() hookGate { return hookGate{active: true, done: make(chan struct{})} }

func (g *hookGate) acquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.active {
		return false
	}
	g.inflight++
	return true
}

func (g *hookGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inflight--
	g.finishIfDrained()
}

func (g *hookGate) acquireSnapshot() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.active {
		return false
	}
	g.snapshots++
	return true
}

func (g *hookGate) releaseSnapshot() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.snapshots--
	g.finishIfDrained()
}

func (g *hookGate) retire() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active = false
	g.finishIfDrained()
	return g.done
}

func (g *hookGate) finishIfDrained() {
	if !g.active && g.inflight == 0 && g.snapshots == 0 && !g.finished {
		g.finished = true
		close(g.done)
	}
}

// NewRuntimeHookRegistry creates an empty registry for removable hooks.
func NewRuntimeHookRegistry() *RuntimeHookRegistry { return &RuntimeHookRegistry{} }

// Register adds hooks in registration order and returns their owner handle.
func (r *RuntimeHookRegistry) Register(hooks RuntimeHooks) (*HookRegistration, error) {
	return r.registerOwned(hooks, "", 0)
}

// RegisterScoped registers hooks owned by one component generation.
func (r *RuntimeHookRegistry) RegisterScoped(hooks RuntimeHooks, owner string, generation uint64) (*HookRegistration, error) {
	if owner == "" || generation == 0 {
		return nil, errors.New("agent: scoped hooks require an owner and generation")
	}
	return r.registerOwned(hooks, owner, generation)
}

func (r *RuntimeHookRegistry) registerOwned(hooks RuntimeHooks, owner string, generation uint64) (*HookRegistration, error) {
	if r == nil {
		return nil, errors.New("agent: runtime hook registry is nil")
	}
	registration := &HookRegistration{registry: r, hooks: hooks, owner: owner, generation: generation, gate: newHookGate()}
	r.mu.Lock()
	r.registrations = append(r.registrations, registration)
	r.revision++
	r.mu.Unlock()
	return registration, nil
}

// Close removes the hooks from future snapshots and waits for callbacks that
// already entered. A timeout does not reactivate the registration.
func (r *HookRegistration) Close(ctx context.Context) error {
	if r == nil || r.registry == nil {
		return nil
	}
	registry := r.registry
	registry.mu.Lock()
	for i, registration := range registry.registrations {
		if registration == r {
			registry.registrations = append(registry.registrations[:i], registry.registrations[i+1:]...)
			registry.revision++
			break
		}
	}
	done := r.gate.retire()
	registry.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *RuntimeHookRegistry) snapshot() []RuntimeHooks {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	registrations := make([]*HookRegistration, 0, len(r.registrations))
	for _, registration := range r.registrations {
		if registration.owner == "" || r.activeOwners[registration.owner] == registration.generation {
			registrations = append(registrations, registration)
		}
	}
	r.mu.RUnlock()
	out := make([]RuntimeHooks, 0, len(registrations))
	for _, registration := range registrations {
		out = append(out, registration.snapshot())
	}
	return out
}

func (r *HookRegistration) snapshot() RuntimeHooks {
	hooks := r.hooks
	var out RuntimeHooks
	if hooks.BeforeRequest != nil {
		out.BeforeRequest = func(ctx context.Context, request *providers.StreamRequest) (*HookedResponse, error) {
			if !r.gate.acquire() {
				return nil, nil
			}
			defer r.gate.release()
			return hooks.BeforeRequest(ctx, request)
		}
	}
	if hooks.AfterRequest != nil {
		out.AfterRequest = func(ctx context.Context, request providers.StreamRequest, message ai.Message, usage ai.Usage, err error) error {
			if !r.gate.acquire() {
				return nil
			}
			defer r.gate.release()
			return hooks.AfterRequest(ctx, request, message, usage, err)
		}
	}
	if hooks.OnError != nil {
		out.OnError = func(ctx context.Context, phase ErrorPhase, err error) error {
			if !r.gate.acquire() {
				return nil
			}
			defer r.gate.release()
			return hooks.OnError(ctx, phase, err)
		}
	}
	if hooks.BeforeMessage != nil {
		out.BeforeMessage = func(ctx context.Context, message *ai.Message) error {
			if !r.gate.acquire() {
				return nil
			}
			defer r.gate.release()
			return hooks.BeforeMessage(ctx, message)
		}
	}
	if hooks.BeforeTurn != nil {
		out.BeforeTurn = func(ctx context.Context, turn TurnContext) error {
			if !r.gate.acquire() {
				return nil
			}
			defer r.gate.release()
			return hooks.BeforeTurn(ctx, turn)
		}
	}
	if hooks.AfterTurn != nil {
		out.AfterTurn = func(ctx context.Context, turn TurnContext, result RunResult) error {
			if !r.gate.acquire() {
				return nil
			}
			defer r.gate.release()
			return hooks.AfterTurn(ctx, turn, result)
		}
	}
	if hooks.BeforeTool != nil {
		out.BeforeTool = func(ctx context.Context, call *ai.ToolCall) error {
			if !r.gate.acquire() {
				return nil
			}
			defer r.gate.release()
			return hooks.BeforeTool(ctx, call)
		}
	}
	if hooks.AfterTool != nil {
		out.AfterTool = func(ctx context.Context, call ai.ToolCall, result *ai.ToolResult) error {
			if !r.gate.acquire() {
				return nil
			}
			defer r.gate.release()
			return hooks.AfterTool(ctx, call, result)
		}
	}
	if hooks.BeforeModel != nil {
		out.BeforeModel = func(ctx context.Context, model *ai.Model) error {
			if !r.gate.acquire() {
				return nil
			}
			defer r.gate.release()
			return hooks.BeforeModel(ctx, model)
		}
	}
	if hooks.AfterModel != nil {
		out.AfterModel = func(ctx context.Context, model ai.Model, err error) error {
			if !r.gate.acquire() {
				return nil
			}
			defer r.gate.release()
			return hooks.AfterModel(ctx, model, err)
		}
	}
	if hooks.BeforeCompaction != nil {
		out.BeforeCompaction = func(ctx context.Context) error {
			if !r.gate.acquire() {
				return nil
			}
			defer r.gate.release()
			return hooks.BeforeCompaction(ctx)
		}
	}
	if hooks.AfterCompaction != nil {
		out.AfterCompaction = func(ctx context.Context, result compaction.Result, err error) {
			if !r.gate.acquire() {
				return
			}
			defer r.gate.release()
			hooks.AfterCompaction(ctx, result, err)
		}
	}
	if hooks.OnSession != nil {
		out.OnSession = func(ctx context.Context, event SessionEvent) error {
			if !r.gate.acquire() {
				return nil
			}
			defer r.gate.release()
			return hooks.OnSession(ctx, event)
		}
	}
	if hooks.AfterRun != nil {
		out.AfterRun = func(ctx context.Context, result RunResult) error {
			if !r.gate.acquire() {
				return nil
			}
			defer r.gate.release()
			return hooks.AfterRun(ctx, result)
		}
	}
	if hooks.BeforeComplete != nil {
		out.BeforeComplete = func(ctx context.Context, result RunResult) (CompletionDecision, error) {
			if !r.gate.acquire() {
				return CompletionAllow, nil
			}
			defer r.gate.release()
			return hooks.BeforeComplete(ctx, result)
		}
	}
	if hooks.Resources != nil {
		out.Resources = func(ctx context.Context) ([]ai.Message, error) {
			if !r.gate.acquire() {
				return nil, nil
			}
			defer r.gate.release()
			return hooks.Resources(ctx)
		}
	}
	if hooks.Tools != nil {
		out.Tools = func(ctx context.Context) ([]ai.Tool, error) {
			if !r.gate.acquire() {
				return nil, nil
			}
			defer r.gate.release()
			return hooks.Tools(ctx)
		}
	}
	return out
}
