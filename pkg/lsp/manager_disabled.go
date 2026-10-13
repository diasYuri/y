//go:build !feature_lsp

package lsp

import (
	"context"
)

// Manager is unavailable when the LSP feature is not compiled.
type Manager struct{}

// Available reports whether this build includes the language-server client.
func Available() bool { return false }

// NewManager creates an unavailable language-server manager.
func NewManager(string, []ServerConfig, StartAuthorizer) *Manager { return &Manager{} }

// OpenDocument reports that the LSP feature is not compiled.
func (*Manager) OpenDocument(context.Context, string, string, string) (Document, error) {
	return Document{}, ErrUnavailable
}

// GetClient reports that the LSP feature is not compiled.
func (*Manager) GetClient(context.Context, string, string) (*Client, string, bool, error) {
	return nil, "", false, ErrUnavailable
}

// Running always returns false when LSP support is not compiled.
func (*Manager) Running(string, string) bool { return false }

// Close is safe for an unavailable manager.
func (*Manager) Close(context.Context) error { return nil }
