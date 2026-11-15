package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// RegistrySnapshot is an immutable set of tool descriptors and dispatch
// handlers captured from one registry view.
type RegistrySnapshot struct {
	registry *Registry
	entries  map[string]registryEntry
	revision uint64
	release  sync.Once
}

// Snapshot captures host-lifetime tools and the component generations selected
// by owners. The returned snapshot must be closed to release registration leases.
func (r *Registry) Snapshot(owners map[string]uint64) (*RegistrySnapshot, error) {
	if r == nil {
		return nil, errors.New("tools: registry is nil")
	}
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return nil, toolError("registry_closed", "tool registry is closed", ErrInvalidTool)
	}
	entries := r.entriesForOwnersLocked(owners)
	selected := make(map[string]registryEntry, len(entries))
	for name, entry := range entries {
		if !entry.registration.gate.acquireSnapshot() {
			for _, acquired := range selected {
				acquired.registration.gate.releaseSnapshot()
			}
			r.mu.RUnlock()
			return nil, toolError("tool_not_found", "tool registry changed while taking snapshot", ErrToolNotFound)
		}
		selected[name] = entry
	}
	revision := r.revision
	r.mu.RUnlock()
	return &RegistrySnapshot{registry: r, entries: selected, revision: revision}, nil
}

// Revision returns the registry registration revision captured by this view.
func (s *RegistrySnapshot) Revision() uint64 {
	if s == nil {
		return 0
	}
	return s.revision
}

// Identity returns an opaque process-local identity for the selected entries.
// It stays stable when unrelated registrations outside this snapshot change.
func (s *RegistrySnapshot) Identity() string {
	if s == nil {
		return ""
	}
	names := make([]string, 0, len(s.entries))
	for name := range s.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	var selected strings.Builder
	for _, name := range names {
		entry := s.entries[name]
		_, _ = fmt.Fprintf(&selected, "%s\x00%p\x00%s\x00%d\x00", name, entry.registration, entry.owner, entry.generation)
	}
	sum := sha256.Sum256([]byte(selected.String()))
	return hex.EncodeToString(sum[:])
}

// List returns copied tool descriptors in stable name order.
func (s *RegistrySnapshot) List() []ToolDescriptor {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.entries))
	for name := range s.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ToolDescriptor, 0, len(names))
	for _, name := range names {
		out = append(out, cloneDescriptor(s.entries[name].desc))
	}
	return out
}

// GetExecutionMode returns the mode captured for name.
func (s *RegistrySnapshot) GetExecutionMode(name string) ExecutionMode {
	if s == nil {
		return ExecutionParallel
	}
	entry, ok := s.entries[name]
	if !ok || entry.desc.ExecutionMode == "" {
		return ExecutionParallel
	}
	return entry.desc.ExecutionMode
}

// Handle executes the handler captured in this snapshot, even after a newer
// registry view has been published.
func (s *RegistrySnapshot) Handle(ctx context.Context, request ToolRequest) (ToolResponse, error) {
	if s == nil || s.registry == nil {
		return ToolResponse{}, toolError("tool_not_found", "tool snapshot is unavailable", ErrToolNotFound)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	entry, ok := s.entries[request.Name]
	if !ok {
		return ToolResponse{}, toolError("tool_not_found", "tool not found in captured snapshot", ErrToolNotFound)
	}
	return s.registry.handleCaptured(ctx, request, cloneDescriptor(entry.desc), entry.handler)
}

// Owner returns the component ID and generation that contributed the captured
// tool. Host-lifetime tools return an empty owner and generation zero.
func (s *RegistrySnapshot) Owner(name string) (string, uint64, bool) {
	if s == nil {
		return "", 0, false
	}
	entry, ok := s.entries[name]
	if !ok {
		return "", 0, false
	}
	return entry.owner, entry.generation, true
}

// Close releases registration leases. It is safe to call repeatedly.
func (s *RegistrySnapshot) Close() error {
	if s == nil {
		return nil
	}
	s.release.Do(func() {
		for _, entry := range s.entries {
			entry.registration.gate.releaseSnapshot()
		}
	})
	return nil
}
