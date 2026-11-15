package tools

import (
	"context"
	"sync"
)

type registrationGate struct {
	mu        sync.Mutex
	active    bool
	inflight  int
	snapshots int
	done      chan struct{}
	finished  bool
}

func newRegistrationGate() *registrationGate {
	return &registrationGate{active: true, done: make(chan struct{})}
}

func (g *registrationGate) acquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.active {
		return false
	}
	g.inflight++
	return true
}

func (g *registrationGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inflight--
	g.finishIfDrained()
}

func (g *registrationGate) acquireSnapshot() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.active {
		return false
	}
	g.snapshots++
	return true
}

func (g *registrationGate) releaseSnapshot() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.snapshots--
	g.finishIfDrained()
}

func (g *registrationGate) retire() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active = false
	g.finishIfDrained()
	return g.done
}

func (g *registrationGate) finishIfDrained() {
	if !g.active && g.inflight == 0 && g.snapshots == 0 && !g.finished {
		g.finished = true
		close(g.done)
	}
}

// ToolRegistration owns one registry entry. Closing it removes the tool from
// future listings and dispatches, then waits for calls already using it.
type ToolRegistration struct {
	registry   *Registry
	name       string
	owner      string
	generation uint64
	gate       *registrationGate
}

// Close retires the tool registration. A timeout only stops this wait; the
// registration remains retired and a later Close call can observe its drain.
func (r *ToolRegistration) Close(ctx context.Context) error {
	if r == nil || r.registry == nil || r.gate == nil {
		return nil
	}
	registry := r.registry
	registry.mu.Lock()
	if r.owner == "" {
		if entry, ok := registry.entries[r.name]; ok && entry.registration == r {
			delete(registry.entries, r.name)
		}
	} else {
		key := scopedToolKey{name: r.name, owner: r.owner, generation: r.generation}
		if entry, ok := registry.scopedEntries[key]; ok && entry.registration == r {
			delete(registry.scopedEntries, key)
			registry.refreshToolOwnerLocked(r.name)
		}
	}
	registry.revision++
	registry.clearToolCacheLocked(r.name)
	done := r.gate.retire()
	registry.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-done:
		registry.mu.Lock()
		delete(registry.registrations, r)
		registry.mu.Unlock()
		return nil
	default:
	}
	select {
	case <-done:
		registry.mu.Lock()
		delete(registry.registrations, r)
		registry.mu.Unlock()
		return nil
	case <-ctx.Done():
		go func() {
			<-done
			registry.mu.Lock()
			delete(registry.registrations, r)
			registry.mu.Unlock()
		}()
		return ctx.Err()
	}
}

func (r *Registry) clearToolCacheLocked(name string) {
	for key := range r.cache {
		if key.name == name {
			delete(r.cache, key)
		}
	}
}
