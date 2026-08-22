//go:build !feature_lsp

package lsp

import (
	"context"
	"encoding/json"
	"io"
)

// Message is the base LSP JSON-RPC message.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

// ResponseError is an LSP error response.
type ResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ResponseMessage is a parsed LSP response.
type ResponseMessage struct {
	Result json.RawMessage
	Error  *ResponseError
}

// Client is an unavailable LSP client for builds without feature_lsp.
type Client struct{}

// NewClient creates an unavailable client.
func NewClient(io.Writer, io.Reader) *Client { return &Client{} }

func (*Client) Initialize(context.Context, string) (json.RawMessage, error) {
	return nil, ErrUnavailable
}

func (*Client) Hover(context.Context, string, int, int) (json.RawMessage, error) {
	return nil, ErrUnavailable
}

func (*Client) Definition(context.Context, string, int, int) (json.RawMessage, error) {
	return nil, ErrUnavailable
}

func (*Client) Shutdown(context.Context) error { return ErrUnavailable }
