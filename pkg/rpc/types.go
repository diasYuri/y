//go:build feature_rpc

package rpc

import (
	"encoding/json"
	"fmt"
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

// Notification is a JSON-RPC 2.0 notification (no ID) sent over SSE.
type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// Error codes.
const (
	ErrParseError     = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternalError  = -32603
)

func newError(code int, message string, data ...any) *ErrorObj {
	e := &ErrorObj{Code: code, Message: message}
	if len(data) > 0 && data[0] != nil {
		e.Data = data[0]
	}
	return e
}

func newResponse(id json.RawMessage, result any) Response {
	return Response{JSONRPC: "2.0", ID: id, Result: result}
}

func newErrorResponse(id json.RawMessage, err *ErrorObj) Response {
	return Response{JSONRPC: "2.0", ID: id, Error: err}
}

func (e *ErrorObj) Error() string {
	if e == nil {
		return "nil"
	}
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// IsNotification reports whether req is a JSON-RPC notification (no ID).
func (req *Request) IsNotification() bool {
	return len(req.ID) == 0 || string(req.ID) == "null"
}
