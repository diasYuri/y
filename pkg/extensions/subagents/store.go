package subagents

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

var (
	// ErrRecordNotFound indicates that a subagent record does not exist.
	ErrRecordNotFound = errors.New("subagent record not found")
	// ErrRecordConflict indicates that a store update used an old version.
	ErrRecordConflict = errors.New("subagent record version conflict")
)

// Store persists subagent metadata. Durable stores should implement the same
// optimistic version semantics as agent.StateStore: Create stores version one
// and Save increments the version only when expectedVersion matches. List with
// an empty parent ID must return all records so Manager.Recover can requeue
// interrupted durable work. The Request field in a Record contains the prompt
// and non-secret execution scope needed to recover queued work; API keys are
// intentionally not part of SpawnRequest.
type Store interface {
	Create(context.Context, Record) error
	Load(context.Context, string) (Record, error)
	List(context.Context, string) ([]Record, error)
	Save(context.Context, string, uint64, Record) error
	Delete(context.Context, string) error
}

// MemoryStore is a concurrency-safe metadata store for ephemeral runs,
// tests, and single-process applications. It does not survive a process
// restart and should not be used as the only store for durable children.
type MemoryStore struct {
	mu      sync.RWMutex
	records map[string]Record
}

// NewMemoryStore creates an empty in-memory subagent store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[string]Record)}
}

// Create inserts a new record at version one.
func (s *MemoryStore) Create(ctx context.Context, record Record) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s == nil {
		return errors.New("subagent store is nil")
	}
	if strings.TrimSpace(record.ID) == "" {
		return errors.New("subagent record ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = make(map[string]Record)
	}
	if _, exists := s.records[record.ID]; exists {
		return ErrRecordConflict
	}
	record.Version = 1
	s.records[record.ID] = cloneRecord(record)
	return nil
}

// Load returns a deep copy of one record.
func (s *MemoryStore) Load(ctx context.Context, id string) (Record, error) {
	if err := contextErr(ctx); err != nil {
		return Record{}, err
	}
	if s == nil {
		return Record{}, errors.New("subagent store is nil")
	}
	s.mu.RLock()
	record, ok := s.records[id]
	s.mu.RUnlock()
	if !ok {
		return Record{}, ErrRecordNotFound
	}
	return cloneRecord(record), nil
}

// List returns records owned by parentID in creation order. An empty parentID
// is reserved for manager recovery and returns records for every parent.
func (s *MemoryStore) List(ctx context.Context, parentID string) ([]Record, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("subagent store is nil")
	}
	s.mu.RLock()
	result := make([]Record, 0)
	for _, record := range s.records {
		if parentID == "" || record.ParentID == parentID {
			result = append(result, cloneRecord(record))
		}
	}
	s.mu.RUnlock()
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

// Save replaces a record when expectedVersion matches its current version.
func (s *MemoryStore) Save(ctx context.Context, id string, expectedVersion uint64, record Record) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s == nil {
		return errors.New("subagent store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.records[id]
	if !ok {
		return ErrRecordNotFound
	}
	if current.Version != expectedVersion {
		return ErrRecordConflict
	}
	record.ID = id
	record.Version = expectedVersion + 1
	s.records[id] = cloneRecord(record)
	return nil
}

// Delete removes a record. It is used to roll back an enqueue that could not
// be accepted by the bounded worker queue.
func (s *MemoryStore) Delete(ctx context.Context, id string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s == nil {
		return errors.New("subagent store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[id]; !ok {
		return ErrRecordNotFound
	}
	delete(s.records, id)
	return nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
