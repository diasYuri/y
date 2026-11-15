// Package sqlite implements local durable SDK stores with a pure Go SQLite
// driver. Paths and lifecycle are supplied by the host; no CLI defaults apply.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Store owns one database connection. Close preserves all durable records.
// Runs and transcripts commit atomically; events and operations have their own
// durable transaction boundaries, so uncertain external effects require explicit
// reconciliation rather than automatic replay.
type Store struct{ db *sql.DB }

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("sqlite: path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return nil, errors.New("sqlite: database must be a regular file")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := u.Query()
	q.Set("_txlock", "immediate")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.initialize(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize sqlite: %w", err)
	}
	return s, nil
}

func (s *Store) initialize(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return errors.New("unsupported future database schema")
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS documents (
 bucket TEXT NOT NULL, key TEXT NOT NULL, version INTEGER NOT NULL, data BLOB NOT NULL,
 PRIMARY KEY(bucket,key));
 CREATE TABLE IF NOT EXISTS events (
 run_id TEXT NOT NULL, sequence INTEGER NOT NULL, event_id TEXT NOT NULL,
 idempotency_key TEXT, data BLOB NOT NULL, PRIMARY KEY(run_id,sequence),
 UNIQUE(run_id,event_id), UNIQUE(run_id,idempotency_key));
 PRAGMA user_version=1;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error                             { return s.db.Close() }
func (s *Store) CheckReadiness(ctx context.Context) error { return s.db.PingContext(ctx) }

func read(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, bucket, key string) ([]byte, uint64, error) {
	var raw []byte
	var version uint64
	err := query.QueryRowContext(ctx, "SELECT data,version FROM documents WHERE bucket=? AND key=?", bucket, key).Scan(&raw, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, os.ErrNotExist
	}
	return raw, version, err
}

func write(ctx context.Context, tx *sql.Tx, bucket, key string, version uint64, raw []byte) error {
	if raw == nil {
		_, err := tx.ExecContext(ctx, "DELETE FROM documents WHERE bucket=? AND key=?", bucket, key)
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO documents(bucket,key,version,data) VALUES(?,?,?,?)
 ON CONFLICT(bucket,key) DO UPDATE SET version=excluded.version,data=excluded.data`, bucket, key, version, raw)
	return err
}

func (s *Store) mutate(ctx context.Context, bucket, key string, change func([]byte, uint64) ([]byte, error)) error {
	if key == "" {
		return errors.New("sqlite: record key is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	raw, version, err := read(ctx, tx, bucket, key)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err = change(raw, version)
	if err != nil {
		return err
	}
	if err := write(ctx, tx, bucket, key, version+1, raw); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PutMetadata(ctx context.Context, key string, value json.RawMessage) error {
	if !json.Valid(value) {
		return errors.New("sqlite: metadata must be valid JSON")
	}
	return s.mutate(ctx, "metadata", key, func([]byte, uint64) ([]byte, error) { return value, nil })
}
func (s *Store) GetMetadata(ctx context.Context, key string) (json.RawMessage, error) {
	raw, _, err := read(ctx, s.db, "metadata", key)
	return raw, err
}

func (s *Store) all(ctx context.Context, bucket string) ([][]byte, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM documents WHERE bucket=? ORDER BY key", bucket)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out [][]byte
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, rows.Err()
}
