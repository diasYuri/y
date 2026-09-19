//go:build !feature_rpc

package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/ai"
)

// Request is a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *ErrorObj       `json:"error,omitempty"`
}

// ErrorObj is a JSON-RPC 2.0 error object.
type ErrorObj struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error formats the JSON-RPC error.
func (e *ErrorObj) Error() string {
	if e == nil {
		return "nil"
	}
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// Notification is a JSON-RPC 2.0 notification.
type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

const (
	ErrParseError     = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternalError  = -32603
)

// StreamEvent is a real-time event emitted during agent execution.
type StreamEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Data      any    `json:"data"`
	Timestamp int64  `json:"timestamp"`
}

// ServerConfig configures the JSON-RPC server.
type ServerConfig struct {
	Addr         string
	Log          io.Writer
	Provider     agent.Provider
	ToolRegistry agent.ToolRegistry
	Model        ai.Model
	SystemPrompt string
	AgentOptions []agent.Option
}

// Server is an unavailable RPC server for builds without feature_rpc.
type Server struct{ cfg ServerConfig }

// NewServer creates an unavailable RPC server.
func NewServer(cfg ServerConfig) *Server { return &Server{cfg: cfg} }

// ListenAndServe reports that RPC support is unavailable.
func (*Server) ListenAndServe() error { return ErrUnavailable }

// Addr returns nil because no listener exists.
func (*Server) Addr() net.Addr { return nil }

// Shutdown is idempotent for the unavailable server.
func (*Server) Shutdown(context.Context) error { return nil }

// IsNotification reports whether req is a JSON-RPC notification.
func (req *Request) IsNotification() bool {
	return len(req.ID) == 0 || string(req.ID) == "null"
}
