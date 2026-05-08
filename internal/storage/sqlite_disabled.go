//go:build !feature_storage_sqlite

package storage

import (
	"errors"
)

// ErrSQLiteNotCompiled is returned when SQLite support is requested but the
// binary was not built with the feature_storage_sqlite tag.
var ErrSQLiteNotCompiled = errors.New("sqlite storage not compiled: build with -tags feature_storage_sqlite and run 'go get modernc.org/sqlite'")

// NewSQLiteStore returns an error indicating SQLite support is not compiled in.
func NewSQLiteStore(_ string) (*SQLiteStore, error) {
	return nil, ErrSQLiteNotCompiled
}

// SQLiteStore is a placeholder when the sqlite feature is not compiled.
type SQLiteStore struct{}
