//go:build feature_lsp

package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// Client communicates with a language server via JSON-RPC.
type Client struct {
	mu      sync.Mutex
	stdin   io.Writer
	stdout  io.Reader
	seq     int
	pending map[int]chan *ResponseMessage
}

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

// NewClient creates an LSP client connected to the given stdin/stdout.
func NewClient(stdin io.Writer, stdout io.Reader) *Client {
	c := &Client{
		stdin:   stdin,
		stdout:  stdout,
		pending: make(map[int]chan *ResponseMessage),
	}
	go c.readLoop()
	return c
}

// Initialize sends the LSP initialize request.
func (c *Client) Initialize(ctx context.Context, rootURI string) (json.RawMessage, error) {
	params, _ := json.Marshal(map[string]any{
		"processId":    nil,
		"rootUri":      rootURI,
		"capabilities": map[string]any{},
	})
	return c.request(ctx, "initialize", params)
}

// Hover requests hover information at a position.
func (c *Client) Hover(ctx context.Context, uri string, line, character int) (json.RawMessage, error) {
	params, _ := json.Marshal(map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": line, "character": character},
	})
	return c.request(ctx, "textDocument/hover", params)
}

// Definition requests go-to-definition at a position.
func (c *Client) Definition(ctx context.Context, uri string, line, character int) (json.RawMessage, error) {
	params, _ := json.Marshal(map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": line, "character": character},
	})
	return c.request(ctx, "textDocument/definition", params)
}

// Shutdown sends the shutdown request.
func (c *Client) Shutdown(ctx context.Context) error {
	_, err := c.request(ctx, "shutdown", nil)
	return err
}

func (c *Client) request(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	c.mu.Lock()
	c.seq++
	id := c.seq
	reply := make(chan *ResponseMessage, 1)
	c.pending[id] = reply
	c.mu.Unlock()

	msg := Message{JSONRPC: "2.0", ID: &id, Method: method, Params: params}
	data, _ := json.Marshal(msg)
	if _, err := fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n%s", len(data), data); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-reply:
		if resp.Error != nil {
			return nil, fmt.Errorf("lsp error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *Client) readLoop() {
	if c.stdout == nil {
		return
	}
	reader := bufio.NewReader(c.stdout)
	for {
		// Read headers until empty line.
		var contentLength int
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			if strings.HasPrefix(line, "Content-Length: ") {
				if n, err := strconv.Atoi(strings.TrimPrefix(line, "Content-Length: ")); err == nil {
					contentLength = n
				}
			}
		}
		if contentLength <= 0 {
			continue
		}
		body := make([]byte, contentLength)
		if _, err := io.ReadFull(reader, body); err != nil {
			return
		}
		var msg Message
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}
		if msg.ID != nil {
			c.mu.Lock()
			reply, ok := c.pending[*msg.ID]
			if ok {
				delete(c.pending, *msg.ID)
			}
			c.mu.Unlock()
			if ok {
				reply <- &ResponseMessage{Result: msg.Result, Error: msg.Error}
			}
		}
	}
}
