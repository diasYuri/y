package lsp

import (
	"context"
)

// ServerConfig describes a language server executable. Arguments are passed
// directly to exec.Command and are never interpreted by a shell.
type ServerConfig struct {
	Language string   `json:"language"`
	Name     string   `json:"name,omitempty"`
	Command  string   `json:"command"`
	Args     []string `json:"args,omitempty"`
}

// Document is a synchronized document and its persistent server connection.
type Document struct {
	Client   *Client
	URI      string
	Text     string
	Language string
	Server   string
	CacheHit bool
}

// StartAuthorizer checks permission once before each server process starts.
type StartAuthorizer func(context.Context, ServerConfig, string) error
