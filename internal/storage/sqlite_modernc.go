//go:build feature_storage_sqlite

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yuri/y/pkg/ai"
	_ "modernc.org/sqlite"
)

// SQLiteStore implements the session storage interface using SQLite.
type SQLiteStore struct {
	dbPath string
	now    func() time.Time
}

// NewSQLiteStore creates a SQLite-backed session store.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	if dbPath == "" {
		dir := os.Getenv("Y_CODING_AGENT_DIR")
		if dir == "" {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".pi", "agent")
		}
		dbPath = filepath.Join(dir, "sessions.db")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	defer db.Close()

	schema := `
CREATE TABLE IF NOT EXISTS sessions (
	id TEXT PRIMARY KEY,
	cwd TEXT NOT NULL,
	created_at TEXT NOT NULL,
	modified_at TEXT NOT NULL,
	truncated INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS messages (
	session_id TEXT NOT NULL,
	seq INTEGER NOT NULL,
	id TEXT NOT NULL,
	timestamp TEXT NOT NULL,
	role TEXT NOT NULL,
	content TEXT,
	tool_calls TEXT,
	tool_result TEXT,
	PRIMARY KEY (session_id, seq),
	FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_sessions_cwd ON sessions(cwd);
CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id);
`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}

	return &SQLiteStore{dbPath: dbPath, now: time.Now}, nil
}

func (s *SQLiteStore) db() (*sql.DB, error) {
	return sql.Open("sqlite", s.dbPath)
}

// List returns summaries for all sessions matching cwd.
func (s *SQLiteStore) List(ctx context.Context, cwd string) ([]SessionSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
		SELECT s.id, s.cwd, s.created_at, s.modified_at, s.truncated, COUNT(m.seq)
		FROM sessions s
		LEFT JOIN messages m ON m.session_id = s.id
		WHERE s.cwd = ?
		GROUP BY s.id
		ORDER BY s.modified_at DESC`, cwd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionSummary
	for rows.Next() {
		var sum SessionSummary
		var created, modified string
		var truncated int
		if err := rows.Scan(&sum.ID, &sum.CWD, &created, &modified, &truncated, &sum.MessageCount); err != nil {
			continue
		}
		sum.Created, _ = time.Parse(time.RFC3339Nano, created)
		sum.Modified, _ = time.Parse(time.RFC3339Nano, modified)
		sum.Truncated = truncated != 0
		sum.Path = s.dbPath + "#" + sum.ID
		out = append(out, sum)
	}
	return out, rows.Err()
}

// Latest returns the most recently modified session for cwd.
func (s *SQLiteStore) Latest(ctx context.Context, cwd string) (*SessionSummary, error) {
	sums, err := s.List(ctx, cwd)
	if err != nil {
		return nil, err
	}
	if len(sums) == 0 {
		return nil, nil
	}
	return &sums[0], nil
}

// Resolve looks up a session by exact ID or prefix.
func (s *SQLiteStore) Resolve(ctx context.Context, cwd, target string) (string, error) {
	if target == "" {
		latest, err := s.Latest(ctx, cwd)
		if err != nil {
			return "", err
		}
		if latest == nil {
			return "", os.ErrNotExist
		}
		return latest.Path, nil
	}
	db, err := s.db()
	if err != nil {
		return "", err
	}
	defer db.Close()

	var id string
	err = db.QueryRowContext(ctx, `SELECT id FROM sessions WHERE cwd = ? AND (id = ? OR id LIKE ? || '%') ORDER BY modified_at DESC LIMIT 1`, cwd, target, target).Scan(&id)
	if err == sql.ErrNoRows {
		return "", os.ErrNotExist
	}
	if err != nil {
		return "", err
	}
	return s.dbPath + "#" + id, nil
}

// SaveTranscript persists a transcript and returns a summary.
func (s *SQLiteStore) SaveTranscript(ctx context.Context, cwd string, messages []ai.Message, maxBytes int64) (SessionSummary, error) {
	if err := ctx.Err(); err != nil {
		return SessionSummary{}, err
	}
	db, err := s.db()
	if err != nil {
		return SessionSummary{}, err
	}
	defer db.Close()

	now := s.now
	if now == nil {
		now = time.Now
	}
	ts := now().UTC()
	id := newSessionID()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return SessionSummary{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sessions (id, cwd, created_at, modified_at, truncated) VALUES (?, ?, ?, ?, 0)`,
		id, cwd, ts.Format(time.RFC3339Nano), ts.Format(time.RFC3339Nano)); err != nil {
		return SessionSummary{}, err
	}

	for i, msg := range messages {
		content, _ := json.Marshal(toSessionMessage(msg))
		var toolCalls, toolResult []byte
		if len(msg.ToolCalls) > 0 {
			toolCalls, _ = json.Marshal(msg.ToolCalls)
		}
		if msg.ToolResult != nil {
			toolResult, _ = json.Marshal(msg.ToolResult)
		}
		msgTs := msg.Timestamp
		if msgTs.IsZero() {
			msgTs = ts
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO messages (session_id, seq, id, timestamp, role, content, tool_calls, tool_result) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, i, newSessionID(), msgTs.Format(time.RFC3339Nano), string(msg.Role), content, toolCalls, toolResult); err != nil {
			return SessionSummary{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return SessionSummary{}, err
	}

	return SessionSummary{
		Path:         s.dbPath + "#" + id,
		ID:           id,
		CWD:          cwd,
		Created:      ts,
		Modified:     ts,
		MessageCount: len(messages),
	}, nil
}

// ReadTranscript reconstructs ai.Message slice from a session ID.
func (s *SQLiteStore) ReadTranscript(ctx context.Context, sessionID string) ([]ai.Message, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		`SELECT role, content, tool_calls, tool_result FROM messages WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ai.Message
	for rows.Next() {
		var role string
		var content, toolCalls, toolResult []byte
		if err := rows.Scan(&role, &content, &toolCalls, &toolResult); err != nil {
			return nil, err
		}
		var sm SessionMessage
		if err := json.Unmarshal(content, &sm); err != nil {
			return nil, err
		}
		msg := ai.Message{
			Role:      ai.Role(role),
			Timestamp: sm.Timestamp,
			Content:   make([]ai.ContentBlock, len(sm.Content)),
		}
		for i, cb := range sm.Content {
			msg.Content[i] = ai.ContentBlock{
				Type:             ai.ContentType(cb.Type),
				Text:             cb.Text,
				Thinking:         cb.Thinking,
				ThinkingRedacted: cb.ThinkingRedacted,
				Signature:        cb.Signature,
				ImageData:        cb.ImageData,
				ImageMIMEType:    cb.ImageMIMEType,
				ProviderMetadata: cb.ProviderMetadata,
			}
		}
		if len(toolCalls) > 0 {
			var calls []ai.ToolCall
			if err := json.Unmarshal(toolCalls, &calls); err == nil {
				msg.ToolCalls = calls
			}
		}
		if len(toolResult) > 0 {
			var tr ai.ToolResult
			if err := json.Unmarshal(toolResult, &tr); err == nil {
				msg.ToolResult = &tr
			}
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

// SessionIDFromPath extracts the session ID from a SQLite store path.
func SessionIDFromPath(path string) string {
	parts := strings.SplitN(path, "#", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return ""
}
