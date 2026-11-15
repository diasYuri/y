// Package memory contains application-owned memory configuration and adapters.
package memory

import publicfs "github.com/diasYuri/y/pkg/memory/filesystem"

// FilesystemStore is retained as a source-compatible alias for the public adapter.
type FilesystemStore = publicfs.FilesystemStore

func NewFilesystemStore(root string) (*FilesystemStore, error) {
	return publicfs.NewFilesystemStore(root)
}
