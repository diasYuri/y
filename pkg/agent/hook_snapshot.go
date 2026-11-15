package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
)

type hookSnapshotEntry struct {
	hooks        RuntimeHooks
	owner        string
	generation   uint64
	registration *HookRegistration
}

// RuntimeHooksSnapshot captures an immutable ordered set of hook callbacks.
// Closing it releases the registration leases held by the snapshot.
type RuntimeHooksSnapshot struct {
	entries  []hookSnapshotEntry
	revision uint64
	close    sync.Once
}

// Snapshot captures host-lifetime hooks and hooks owned by selected component
// generations. Callback order matches registration order.
func (r *RuntimeHookRegistry) Snapshot(owners map[string]uint64) (*RuntimeHooksSnapshot, error) {
	if r == nil {
		return nil, errors.New("agent: runtime hook registry is nil")
	}
	r.mu.RLock()
	entries := make([]hookSnapshotEntry, 0, len(r.registrations))
	for _, registration := range r.registrations {
		if registration.owner != "" && owners[registration.owner] != registration.generation {
			continue
		}
		if !registration.gate.acquireSnapshot() {
			for _, entry := range entries {
				entry.registration.gate.releaseSnapshot()
			}
			r.mu.RUnlock()
			return nil, errors.New("agent: hook registry changed while taking snapshot")
		}
		entries = append(entries, hookSnapshotEntry{
			hooks: registration.hooks, owner: registration.owner,
			generation: registration.generation, registration: registration,
		})
	}
	revision := r.revision
	r.mu.RUnlock()
	return &RuntimeHooksSnapshot{entries: entries, revision: revision}, nil
}

// Revision returns the hook registration revision captured by this snapshot.
func (s *RuntimeHooksSnapshot) Revision() uint64 {
	if s == nil {
		return 0
	}
	return s.revision
}

// Identity returns an opaque process-local identity for the captured hooks.
func (s *RuntimeHooksSnapshot) Identity() string {
	if s == nil {
		return ""
	}
	var selected strings.Builder
	for i, entry := range s.entries {
		_, _ = fmt.Fprintf(&selected, "%d\x00%p\x00%s\x00%d\x00", i, entry.registration, entry.owner, entry.generation)
	}
	sum := sha256.Sum256([]byte(selected.String()))
	return hex.EncodeToString(sum[:])
}

// Hooks returns the callbacks captured by this snapshot in registration order.
func (s *RuntimeHooksSnapshot) Hooks() []RuntimeHooks {
	if s == nil {
		return nil
	}
	out := make([]RuntimeHooks, len(s.entries))
	for i, entry := range s.entries {
		out[i] = entry.hooks
	}
	return out
}

// Owner returns the component identity for a captured registration.
func (s *RuntimeHooksSnapshot) Owner(index int) (string, uint64, bool) {
	if s == nil || index < 0 || index >= len(s.entries) {
		return "", 0, false
	}
	entry := s.entries[index]
	return entry.owner, entry.generation, true
}

// Close releases registration leases and is safe to call repeatedly.
func (s *RuntimeHooksSnapshot) Close() error {
	if s == nil {
		return nil
	}
	s.close.Do(func() {
		for _, entry := range s.entries {
			entry.registration.gate.releaseSnapshot()
		}
	})
	return nil
}

// PublishOwners updates the registry view used by legacy live hook dispatches.
// Existing RuntimeHooksSnapshots are unaffected.
func (r *RuntimeHookRegistry) PublishOwners(owners map[string]uint64) {
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
