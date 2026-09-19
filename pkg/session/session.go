package session

import (
	"context"
	"time"

	"github.com/diasYuri/y/pkg/ai"
)

// SessionSummary describes one persisted agent transcript.
//
// Path is an implementation-defined locator. File-backed stores typically
// return a filesystem path, while database-backed stores may return another
// stable locator understood by that store.
type SessionSummary struct {
	Path         string    `json:"path"`
	ID           string    `json:"id"`
	CWD          string    `json:"cwd"`
	Created      time.Time `json:"created"`
	Modified     time.Time `json:"modified"`
	MessageCount int       `json:"message_count"`
	ByteSize     int64     `json:"byte_size"`
	Truncated    bool      `json:"truncated"`
}

// Store persists and locates agent transcripts.
//
// Implementations may use files, a database, or another backend. The
// serialization format and storage location are intentionally not part of
// this contract.
type Store interface {
	List(context.Context, string) ([]SessionSummary, error)
	Latest(context.Context, string) (*SessionSummary, error)
	Resolve(context.Context, string, string) (string, error)
	SaveTranscript(context.Context, string, []ai.Message, int64) (SessionSummary, error)
}
