package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yuri/y/pkg/ai"
)

func (s *JSONLStore) AcquireLease(ctx context.Context, runID, owner string, ttl time.Duration) (Lease, error) {
	if err := contextErr(ctx); err != nil {
		return Lease{}, err
	}
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(owner) == "" || ttl <= 0 {
		return Lease{}, errors.New("agent: run ID, lease owner and positive TTL are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, runID)
	if err != nil {
		return Lease{}, err
	}
	defer release()
	current, err := s.loadLeaseUnlocked(runID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Lease{}, err
	}
	now := time.Now().UTC()
	if current.Owner != "" && current.ExpiresAt.After(now) && current.Owner != owner {
		return Lease{}, ErrLeaseHeld
	}
	lease := Lease{RunID: runID, Owner: owner, ExpiresAt: now.Add(ttl)}
	if err := s.saveLeaseUnlocked(lease); err != nil {
		return Lease{}, err
	}
	return lease, nil
}

func (s *JSONLStore) RenewLease(ctx context.Context, lease Lease, ttl time.Duration) (Lease, error) {
	if err := contextErr(ctx); err != nil {
		return Lease{}, err
	}
	if lease.RunID == "" || lease.Owner == "" || ttl <= 0 {
		return Lease{}, errors.New("agent: valid lease and positive TTL are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, lease.RunID)
	if err != nil {
		return Lease{}, err
	}
	defer release()
	current, err := s.loadLeaseUnlocked(lease.RunID)
	if errors.Is(err, os.ErrNotExist) {
		return Lease{}, ErrLeaseNotFound
	}
	if err != nil {
		return Lease{}, err
	}
	if current.Owner != lease.Owner || !current.ExpiresAt.After(time.Now().UTC()) {
		return Lease{}, ErrLeaseNotFound
	}
	current.ExpiresAt = time.Now().UTC().Add(ttl)
	if err := s.saveLeaseUnlocked(current); err != nil {
		return Lease{}, err
	}
	return current, nil
}

func (s *JSONLStore) ReleaseLease(ctx context.Context, lease Lease) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if lease.RunID == "" || lease.Owner == "" {
		return ErrLeaseNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireRunLock(ctx, lease.RunID)
	if err != nil {
		return err
	}
	defer release()
	current, err := s.loadLeaseUnlocked(lease.RunID)
	if errors.Is(err, os.ErrNotExist) {
		return ErrLeaseNotFound
	}
	if err != nil {
		return err
	}
	if current.Owner != lease.Owner {
		return ErrLeaseNotFound
	}
	return os.Remove(filepath.Join(s.runDir(lease.RunID), "lease.json"))
}

func (s *JSONLStore) loadLeaseUnlocked(runID string) (Lease, error) {
	raw, err := s.readFile(runID, "lease.json")
	if err != nil {
		return Lease{}, err
	}
	var lease Lease
	if err := json.Unmarshal(raw, &lease); err != nil {
		return Lease{}, fmt.Errorf("decode lease: %w", err)
	}
	return lease, nil
}

func (s *JSONLStore) saveLeaseUnlocked(lease Lease) error {
	return atomicJSONWrite(filepath.Join(s.runDir(lease.RunID), "lease.json"), lease)
}

type transcriptRecord struct {
	SchemaVersion int          `json:"schema_version"`
	Version       uint64       `json:"version"`
	Messages      []ai.Message `json:"messages,omitempty"`
}

func (s *JSONLStore) LoadTranscript(ctx context.Context, runID string) ([]ai.Message, error) {
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
	raw, err := s.readFile(runID, "transcript.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrTranscriptNotFound
	}
	if err != nil {
		return nil, err
	}
	var record transcriptRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("decode transcript: %w", err)
	}
	for i := range record.Messages {
		message, err := ai.MigrateMessage(record.Messages[i])
		if err != nil {
			return nil, err
		}
		record.Messages[i] = message
	}
	return cloneMessages(record.Messages), nil
}

func (s *JSONLStore) SaveTranscript(ctx context.Context, runID string, expectedVersion uint64, messages []ai.Message) error {
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
	currentVersion := uint64(0)
	raw, readErr := s.readFile(runID, "transcript.json")
	if readErr == nil {
		var current transcriptRecord
		if err := json.Unmarshal(raw, &current); err != nil {
			return fmt.Errorf("decode transcript: %w", err)
		}
		currentVersion = current.Version
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if readErr == nil && currentVersion != expectedVersion {
		return ErrVersionConflict
	}
	return atomicJSONWrite(filepath.Join(s.runDir(runID), "transcript.json"), transcriptRecord{
		SchemaVersion: ai.CurrentSchemaVersion,
		Version:       expectedVersion,
		Messages:      cloneMessages(messages),
	})
}
