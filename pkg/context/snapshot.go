package context

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ResolverSnapshot resolves requests using a fixed source set. Close releases
// the source registration leases held by the snapshot.
type ResolverSnapshot struct {
	resolver *Resolver
	entries  []*sourceEntry
	revision uint64
	close    sync.Once
}

// Snapshot captures host-lifetime sources and only the scoped sources owned by
// the selected component generations.
func (r *Resolver) Snapshot(owners map[string]uint64) (*ResolverSnapshot, error) {
	if r == nil {
		return nil, errors.New("context resolver is nil")
	}
	r.mu.RLock()
	entries := make([]*sourceEntry, 0, len(r.sources))
	for _, entry := range r.sources {
		if entry.owner != "" && owners[entry.owner] != entry.generation {
			continue
		}
		if !entry.gate.acquireSnapshot() {
			for _, acquired := range entries {
				acquired.gate.releaseSnapshot()
			}
			r.mu.RUnlock()
			return nil, errors.New("context resolver changed while taking snapshot")
		}
		entries = append(entries, entry)
	}
	revision := r.revision
	r.mu.RUnlock()
	view := &Resolver{sources: entries, cache: make(map[string]Result)}
	return &ResolverSnapshot{resolver: view, entries: entries, revision: revision}, nil
}

// Revision returns the source registration revision captured by this view.
func (s *ResolverSnapshot) Revision() uint64 {
	if s == nil {
		return 0
	}
	return s.revision
}

// Identity returns an opaque process-local identity for the captured sources.
// It does not change when an unrelated source is registered elsewhere.
func (s *ResolverSnapshot) Identity() string {
	if s == nil {
		return ""
	}
	var selected strings.Builder
	for i, entry := range s.entries {
		_, _ = fmt.Fprintf(&selected, "%d\x00%p\x00%s\x00%s\x00%d\x00", i, entry, sourceName(entry.source, i), entry.owner, entry.generation)
	}
	sum := sha256.Sum256([]byte(selected.String()))
	return hex.EncodeToString(sum[:])
}

// Resolve uses the source set captured by this snapshot.
func (s *ResolverSnapshot) Resolve(ctx context.Context, request Request) (Result, error) {
	if s == nil || s.resolver == nil {
		return Result{}, errors.New("context resolver snapshot is nil")
	}
	return s.resolver.resolveCaptured(ctx, request, s.entries)
}

// Owners returns the component generations that contributed context sources.
func (s *ResolverSnapshot) Owners() map[string]uint64 {
	if s == nil {
		return nil
	}
	owners := make(map[string]uint64)
	for _, entry := range s.entries {
		if entry.owner != "" {
			owners[entry.owner] = entry.generation
		}
	}
	return owners
}

// Origins reports the source registrations captured by this snapshot. The
// component owner and generation identify the implementation without exposing
// the source object itself.
func (s *ResolverSnapshot) Origins() []SourceOrigin {
	if s == nil {
		return nil
	}
	origins := make([]SourceOrigin, 0, len(s.entries))
	for i, entry := range s.entries {
		name := sourceName(entry.source, i)
		origins = append(origins, SourceOrigin{
			Index: i, Name: name, Owner: entry.owner, Generation: entry.generation,
		})
	}
	return origins
}

// Close releases source leases and is safe to call repeatedly.
func (s *ResolverSnapshot) Close() error {
	if s == nil {
		return nil
	}
	s.close.Do(func() {
		for _, entry := range s.entries {
			entry.gate.releaseSnapshot()
		}
	})
	return nil
}

// PublishOwners updates the resolver's live source view for legacy callers.
// Captured ResolverSnapshots are unaffected.
func (r *Resolver) PublishOwners(owners map[string]uint64) {
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
