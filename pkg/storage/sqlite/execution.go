package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/tools"
)

func (s *Store) Load(ctx context.Context, id string) (agent.Snapshot, error) {
	raw, _, err := read(ctx, s.db, "runs", id)
	if errors.Is(err, os.ErrNotExist) {
		return agent.Snapshot{}, agent.ErrStateNotFound
	}
	if err != nil {
		return agent.Snapshot{}, err
	}
	var state agent.Snapshot
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, err
	}
	state, err = state.Migrate()
	if err != nil {
		return state, err
	}
	return state, state.Validate()
}

func (s *Store) Save(ctx context.Context, id string, expected uint64, state agent.Snapshot) error {
	if id == "" || state.RunID != id {
		return agent.ErrInvalidSnapshot
	}
	var err error
	state, err = state.Migrate()
	if err != nil {
		return err
	}
	state.Version = expected + 1
	state.UpdatedAt = time.Now().UTC()
	if err := state.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, version, err := read(ctx, tx, "runs", id)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if version != expected {
		return agent.ErrVersionConflict
	}
	if err := write(ctx, tx, "runs", id, state.Version, raw); err != nil {
		return err
	}
	transcript, err := json.Marshal(state.Agent.Transcript)
	if err != nil {
		return err
	}
	if err := write(ctx, tx, "transcripts", id, state.Version, transcript); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LoadTranscript(ctx context.Context, id string) ([]ai.Message, error) {
	raw, _, err := read(ctx, s.db, "transcripts", id)
	if errors.Is(err, os.ErrNotExist) {
		return nil, agent.ErrTranscriptNotFound
	}
	if err != nil {
		return nil, err
	}
	var messages []ai.Message
	err = json.Unmarshal(raw, &messages)
	return messages, err
}
func (s *Store) SaveTranscript(ctx context.Context, id string, expected uint64, messages []ai.Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, version, err := read(ctx, tx, "transcripts", id)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if version > expected {
		return agent.ErrVersionConflict
	}
	raw, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	if err := write(ctx, tx, "transcripts", id, expected, raw); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) BeginOperation(ctx context.Context, requested agent.OperationRecord) (agent.OperationRecord, bool, error) {
	if requested.IdempotencyKey == "" || requested.RunID == "" || requested.ToolName == "" || requested.ArgumentsHash == "" {
		return requested, false, agent.ErrInvalidEvent
	}
	var result agent.OperationRecord
	acquired := false
	err := s.mutate(ctx, "operations", requested.IdempotencyKey, func(raw []byte, _ uint64) ([]byte, error) {
		if raw != nil {
			if err := json.Unmarshal(raw, &result); err != nil {
				return nil, err
			}
			if result.RunID != requested.RunID || result.ToolCallID != requested.ToolCallID || result.ToolName != requested.ToolName || result.ArgumentsHash != requested.ArgumentsHash {
				return nil, agent.ErrIdempotencyConflict
			}
			return raw, nil
		}
		result = requested
		result.SchemaVersion = agent.CurrentOperationSchemaVersion
		result.State = agent.OperationPending
		result.StartedAt = time.Now().UTC()
		acquired = true
		return json.Marshal(result)
	})
	return result, acquired, err
}

func (s *Store) CompleteOperation(ctx context.Context, key string, response tools.ToolResponse, operationErr error) error {
	return s.mutate(ctx, "operations", key, func(raw []byte, _ uint64) ([]byte, error) {
		if raw == nil {
			return nil, agent.ErrOperationNotFound
		}
		var record agent.OperationRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		if record.State != agent.OperationPending {
			return raw, nil
		}
		record.Response = &response
		record.State = agent.OperationCompleted
		record.CompletedAt = time.Now().UTC()
		if operationErr != nil {
			record.State = agent.OperationFailed
			record.Error = &agent.EventError{Message: operationErr.Error()}
		}
		return json.Marshal(record)
	})
}

// ReleaseOperation is used only when authorization prevented the effect.
func (s *Store) ReleaseOperation(ctx context.Context, key string) error {
	return s.mutate(ctx, "operations", key, func(raw []byte, _ uint64) ([]byte, error) {
		if raw == nil {
			return nil, agent.ErrOperationNotFound
		}
		var record agent.OperationRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		if record.State != agent.OperationPending {
			return nil, agent.ErrIdempotencyConflict
		}
		return nil, nil
	})
}
func (s *Store) Operations(ctx context.Context, runID string) ([]agent.OperationRecord, error) {
	values, err := s.all(ctx, "operations")
	if err != nil {
		return nil, err
	}
	var out []agent.OperationRecord
	for _, raw := range values {
		var record agent.OperationRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		if record.RunID == runID {
			out = append(out, record)
		}
	}
	return out, nil
}

var _ agent.StateStore = (*Store)(nil)
var _ agent.TranscriptStore = (*Store)(nil)
var _ agent.OperationStore = (*Store)(nil)
