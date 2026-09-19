package memory

import (
	"context"
	"errors"

	ycontext "github.com/diasYuri/y/pkg/context"
	pmemory "github.com/diasYuri/y/pkg/memory"
)

// SQLiteOperationalIndex is the optional operational-index seam. The minimal
// build does not require SQLite; a feature-gated implementation can satisfy
// this interface while Markdown remains the durable human-readable source.
type SQLiteOperationalIndex interface {
	Put(context.Context, pmemory.Memory) error
	Delete(context.Context, string) error
	Get(context.Context, string) (pmemory.Memory, error)
	Version(context.Context, ycontext.Request) (string, error)
}

// ErrSQLiteUnavailable is returned by callers that explicitly request SQLite
// without compiling or injecting an operational index.
var ErrSQLiteUnavailable = errors.New("memory sqlite operational index unavailable")
