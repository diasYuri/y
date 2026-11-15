package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/diasYuri/y/pkg/agent"
)

func (s *Store) Append(ctx context.Context, id string, events []agent.EventEnvelope) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var sequence uint64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM events WHERE run_id=?", id).Scan(&sequence); err != nil {
		return err
	}
	for _, event := range events {
		if event.RunID != id {
			return agent.ErrInvalidEvent
		}
		var existing uint64
		err := tx.QueryRowContext(ctx, "SELECT sequence FROM events WHERE run_id=? AND (event_id=? OR idempotency_key=?)", id, event.EventID, event.IdempotencyKey).Scan(&existing)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		sequence++
		if event.Sequence != 0 && event.Sequence != sequence {
			return agent.ErrVersionConflict
		}
		event.Sequence = sequence
		if err := event.Validate(); err != nil {
			return err
		}
		raw, err := json.Marshal(event)
		if err != nil {
			return err
		}
		var key any
		if event.IdempotencyKey != "" {
			key = event.IdempotencyKey
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO events(run_id,sequence,event_id,idempotency_key,data) VALUES(?,?,?,?,?)", id, sequence, event.EventID, key, raw); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Read(ctx context.Context, id string, after uint64) ([]agent.EventEnvelope, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM events WHERE run_id=? AND sequence>? ORDER BY sequence", id, after)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []agent.EventEnvelope
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event agent.EventEnvelope
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

var _ agent.EventStore = (*Store)(nil)
