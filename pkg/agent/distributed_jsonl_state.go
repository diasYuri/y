package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// JSONLStore is a local development adapter. State is atomically replaced as
// JSON and events are appended as one JSON object per line. It intentionally
// exposes the same contracts that a SQL/object-store adapter would implement.
type JSONLStore struct {
	root string
	mu   sync.Mutex
}

func NewJSONLStore(root string) *JSONLStore { return &JSONLStore{root: root} }

const jsonlLockStaleAfter = 5 * time.Minute

func (s *JSONLStore) runDir(runID string) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(runID))
	return filepath.Join(s.root, "runs", "r-"+encoded)
}

func (s *JSONLStore) legacyRunDir(runID string) (string, bool) {
	if runID == "" || runID == "." || runID == ".." {
		return "", false
	}
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, runID)
	if clean != runID {
		return "", false
	}
	return filepath.Join(s.root, clean), true
}

func (s *JSONLStore) readFile(runID, name string) ([]byte, error) {
	path := filepath.Join(s.runDir(runID), name)
	raw, err := os.ReadFile(path)
	if !errors.Is(err, os.ErrNotExist) {
		return raw, err
	}
	legacyDir, ok := s.legacyRunDir(runID)
	if !ok {
		return nil, err
	}
	return os.ReadFile(filepath.Join(legacyDir, name))
}

func (s *JSONLStore) Load(ctx context.Context, runID string) (Snapshot, error) {
	if err := contextErr(ctx); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return Snapshot{}, err
	}
	defer release()
	raw, err := s.readFile(runID, "state.json")
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, ErrStateNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	var state Snapshot
	if err := json.Unmarshal(raw, &state); err != nil {
		return Snapshot{}, fmt.Errorf("decode state: %w", err)
	}
	if state.RunID != runID {
		return Snapshot{}, fmt.Errorf("%w: stored run_id %q does not match %q", ErrInvalidSnapshot, state.RunID, runID)
	}
	state = normalizeSnapshot(state, runID)
	var migrateErr error
	state, migrateErr = state.Migrate()
	if migrateErr != nil {
		return Snapshot{}, migrateErr
	}
	return state, state.Validate()
}

func (s *JSONLStore) Save(ctx context.Context, runID string, expectedVersion uint64, state Snapshot) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	current, err := s.loadUnlocked(runID)
	switch {
	case errors.Is(err, ErrStateNotFound):
		if expectedVersion != 0 {
			return ErrVersionConflict
		}
	case err != nil:
		return err
	case current.Version != expectedVersion:
		return fmt.Errorf("%w: expected %d, got %d", ErrVersionConflict, expectedVersion, current.Version)
	}
	state = normalizeSnapshot(state, runID)
	var migrateErr error
	state, migrateErr = state.Migrate()
	if migrateErr != nil {
		return migrateErr
	}
	if err := state.Validate(); err != nil {
		return err
	}
	state.Version = expectedVersion + 1
	state.UpdatedAt = time.Now().UTC()
	return atomicJSONWrite(filepath.Join(s.runDir(runID), "state.json"), state)
}

func (s *JSONLStore) loadUnlocked(runID string) (Snapshot, error) {
	raw, err := s.readFile(runID, "state.json")
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, ErrStateNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	var state Snapshot
	if err := json.Unmarshal(raw, &state); err != nil {
		return Snapshot{}, err
	}
	if state.RunID != runID {
		return Snapshot{}, fmt.Errorf("%w: stored run_id %q does not match %q", ErrInvalidSnapshot, state.RunID, runID)
	}
	state = normalizeSnapshot(state, runID)
	var migrateErr error
	state, migrateErr = state.Migrate()
	if migrateErr != nil {
		return Snapshot{}, migrateErr
	}
	return state, state.Validate()
}

func (s *JSONLStore) Append(ctx context.Context, runID string, events []EventEnvelope) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	path := filepath.Join(s.runDir(runID), "events.jsonl")
	contents, err := s.readFile(runID, "events.jsonl")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var existing []EventEnvelope
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event EventEnvelope
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return fmt.Errorf("decode event: %w", err)
		}
		migrated, err := event.Migrate()
		if err != nil {
			return err
		}
		event = migrated
		if event.RunID != runID {
			return fmt.Errorf("%w: stored run_id %q does not match %q", ErrInvalidEvent, event.RunID, runID)
		}
		if err := event.Validate(); err != nil {
			return err
		}
		existing = append(existing, event)
	}
	store := NewInMemoryEventStore()
	store.events[runID] = existing
	if err := store.Append(ctx, runID, events); err != nil {
		return err
	}
	if len(store.events[runID]) == len(existing) {
		return nil
	}
	if err := os.MkdirAll(s.runDir(runID), 0o700); err != nil {
		return err
	}
	var canonical strings.Builder
	for _, event := range store.events[runID] {
		line, err := json.Marshal(event)
		if err != nil {
			return err
		}
		canonical.Write(line)
		canonical.WriteByte('\n')
	}
	// Rewriting the small local log through temp-file/rename makes one Append
	// call atomic even when it contains a batch.
	return atomicWriteBytes(path, []byte(canonical.String()), 0o600)
}

func (s *JSONLStore) Read(ctx context.Context, runID string, after uint64) ([]EventEnvelope, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return nil, err
	}
	defer release()
	raw, err := s.readFile(runID, "events.jsonl")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []EventEnvelope
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event EventEnvelope
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("decode event: %w", err)
		}
		migrated, err := event.Migrate()
		if err != nil {
			return nil, err
		}
		event = migrated
		if event.RunID != runID {
			return nil, fmt.Errorf("%w: stored run_id %q does not match %q", ErrInvalidEvent, event.RunID, runID)
		}
		if err := event.Validate(); err != nil {
			return nil, err
		}
		if event.Sequence > after {
			out = append(out, event)
		}
	}
	return out, nil
}
