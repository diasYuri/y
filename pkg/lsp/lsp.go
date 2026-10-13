//go:build feature_lsp

// Package lsp implements the language-server protocol client used by coding
// tools. Coordinates passed to this package are zero-based LSP coordinates.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

const maxMessageBytes = 64 << 20

// Client communicates with a language server via framed JSON-RPC.
type Client struct {
	mu            sync.Mutex
	writeMu       sync.Mutex
	stdin         io.Writer
	stdout        io.Reader
	seq           int
	pending       map[int]chan *ResponseMessage
	done          chan struct{}
	readErr       error
	closed        bool
	serverRequest func(context.Context, string, json.RawMessage) (any, error)
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

// NewClient creates a client connected to the supplied server pipes.
func NewClient(stdin io.Writer, stdout io.Reader) *Client {
	c := &Client{stdin: stdin, stdout: stdout, pending: make(map[int]chan *ResponseMessage), done: make(chan struct{})}
	if stdout == nil {
		c.readErr = errors.New("LSP client stdout is unavailable")
		c.closed = true
		close(c.done)
		return c
	}
	go c.readLoop()
	return c
}

// SetServerRequestHandler installs the handler for requests initiated by the
// language server. Unhandled requests receive a JSON-RPC null result.
func (c *Client) SetServerRequestHandler(handler func(context.Context, string, json.RawMessage) (any, error)) {
	c.mu.Lock()
	c.serverRequest = handler
	c.mu.Unlock()
}

// Initialize sends initialize and initialized, advertising UTF-16 positions.
func (c *Client) Initialize(ctx context.Context, rootURI string) (json.RawMessage, error) {
	params, _ := json.Marshal(map[string]any{
		"processId":  nil,
		"clientInfo": map[string]string{"name": "y", "version": "1"},
		"rootUri":    rootURI,
		"rootPath":   strings.TrimPrefix(rootURI, "file://"),
		"capabilities": map[string]any{
			"general": map[string]any{"positionEncodings": []string{"utf-16"}},
			"textDocument": map[string]any{
				"definition":     map[string]any{"dynamicRegistration": false},
				"references":     map[string]any{"dynamicRegistration": false},
				"implementation": map[string]any{"dynamicRegistration": false},
				"typeDefinition": map[string]any{"dynamicRegistration": false},
				"documentSymbol": map[string]any{"hierarchicalDocumentSymbolSupport": true},
				"callHierarchy":  map[string]any{"dynamicRegistration": false},
			},
		},
	})
	result, err := c.Request(ctx, "initialize", params)
	if err != nil {
		return nil, err
	}
	if err := c.Notify("initialized", json.RawMessage(`{}`)); err != nil {
		return nil, err
	}
	return result, nil
}

// Request sends an LSP request and returns its structured JSON result.
func (c *Client) Request(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.closed {
		err := c.readErr
		c.mu.Unlock()
		if err == nil {
			err = io.ErrClosedPipe
		}
		return nil, fmt.Errorf("lsp server is not available: %w", err)
	}
	c.seq++
	id := c.seq
	reply := make(chan *ResponseMessage, 1)
	c.pending[id] = reply
	c.mu.Unlock()
	if err := c.write(Message{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		c.removePending(id)
		return nil, err
	}
	select {
	case <-ctx.Done():
		c.removePending(id)
		cancelParams, _ := json.Marshal(map[string]int{"id": id})
		_ = c.Notify("$/cancelRequest", cancelParams)
		return nil, ctx.Err()
	case <-c.done:
		c.mu.Lock()
		err := c.readErr
		c.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return nil, fmt.Errorf("language server stopped: %w", err)
	case response := <-reply:
		if response.Error != nil {
			return nil, fmt.Errorf("lsp error %d: %s", response.Error.Code, response.Error.Message)
		}
		return response.Result, nil
	}
}

// Notify sends an LSP notification.
func (c *Client) Notify(method string, params json.RawMessage) error {
	return c.write(Message{JSONRPC: "2.0", Method: method, Params: params})
}

// Hover requests hover information at an LSP position.
func (c *Client) Hover(ctx context.Context, uri string, line, character int) (json.RawMessage, error) {
	return c.textPositionRequest(ctx, "textDocument/hover", uri, line, character, nil)
}

// Definition requests go-to-definition at an LSP position.
func (c *Client) Definition(ctx context.Context, uri string, line, character int) (json.RawMessage, error) {
	return c.textPositionRequest(ctx, "textDocument/definition", uri, line, character, nil)
}

// References requests references to the symbol at an LSP position.
func (c *Client) References(ctx context.Context, uri string, line, character int, includeDeclaration bool) (json.RawMessage, error) {
	return c.textPositionRequest(ctx, "textDocument/references", uri, line, character, map[string]bool{"includeDeclaration": includeDeclaration})
}

// Implementations requests concrete implementations of the selected symbol.
func (c *Client) Implementations(ctx context.Context, uri string, line, character int) (json.RawMessage, error) {
	return c.textPositionRequest(ctx, "textDocument/implementation", uri, line, character, nil)
}

// TypeDefinition requests the selected symbol's type definition.
func (c *Client) TypeDefinition(ctx context.Context, uri string, line, character int) (json.RawMessage, error) {
	return c.textPositionRequest(ctx, "textDocument/typeDefinition", uri, line, character, nil)
}

// DocumentSymbols returns symbols declared in a document.
func (c *Client) DocumentSymbols(ctx context.Context, uri string) (json.RawMessage, error) {
	params, _ := json.Marshal(map[string]any{"textDocument": map[string]string{"uri": uri}})
	return c.Request(ctx, "textDocument/documentSymbol", params)
}

// WorkspaceSymbols searches symbols indexed by the language server.
func (c *Client) WorkspaceSymbols(ctx context.Context, query string) (json.RawMessage, error) {
	params, _ := json.Marshal(map[string]string{"query": query})
	return c.Request(ctx, "workspace/symbol", params)
}

// CallHierarchy prepares a call hierarchy item at a source position.
func (c *Client) CallHierarchy(ctx context.Context, uri string, line, character int) (json.RawMessage, error) {
	return c.textPositionRequest(ctx, "textDocument/prepareCallHierarchy", uri, line, character, nil)
}

// IncomingCalls returns callers for a prepared call hierarchy item.
func (c *Client) IncomingCalls(ctx context.Context, item json.RawMessage) (json.RawMessage, error) {
	return c.callHierarchyRequest(ctx, "callHierarchy/incomingCalls", item)
}

// OutgoingCalls returns callees for a prepared call hierarchy item.
func (c *Client) OutgoingCalls(ctx context.Context, item json.RawMessage) (json.RawMessage, error) {
	return c.callHierarchyRequest(ctx, "callHierarchy/outgoingCalls", item)
}

// Shutdown sends shutdown followed by exit.
func (c *Client) Shutdown(ctx context.Context) error {
	_, err := c.Request(ctx, "shutdown", nil)
	if err != nil {
		return err
	}
	return c.Notify("exit", nil)
}

// Done is closed when the server's output stream ends or becomes invalid.
func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) textPositionRequest(ctx context.Context, method, uri string, line, character int, extra any) (json.RawMessage, error) {
	params := map[string]any{"textDocument": map[string]string{"uri": uri}, "position": map[string]int{"line": line, "character": character}}
	if extra != nil {
		params["context"] = extra
	}
	raw, _ := json.Marshal(params)
	return c.Request(ctx, method, raw)
}

func (c *Client) callHierarchyRequest(ctx context.Context, method string, item json.RawMessage) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(item, &value); err != nil {
		return nil, err
	}
	params, _ := json.Marshal(map[string]any{"item": value})
	return c.Request(ctx, method, params)
}

func (c *Client) removePending(id int) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) write(message Message) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n", len(data)); err != nil {
		return err
	}
	_, err = c.stdin.Write(data)
	return err
}

func (c *Client) readLoop() {
	defer close(c.done)
	reader := bufio.NewReader(c.stdout)
	for {
		length, err := readContentLength(reader)
		if err != nil {
			c.failPending(err)
			return
		}
		if length <= 0 || length > maxMessageBytes {
			c.failPending(fmt.Errorf("invalid LSP message length %d", length))
			return
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(reader, body); err != nil {
			c.failPending(err)
			return
		}
		var msg Message
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}
		if msg.Method != "" {
			if msg.ID != nil {
				go c.handleServerRequest(msg)
			}
			continue
		}
		if msg.ID == nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[*msg.ID]
		delete(c.pending, *msg.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- &ResponseMessage{Result: msg.Result, Error: msg.Error}
		}
	}
}

func readContentLength(reader *bufio.Reader) (int, error) {
	length := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, parseErr := strconv.Atoi(strings.TrimSpace(value))
			if parseErr != nil {
				return 0, parseErr
			}
			length = n
		}
	}
	return length, nil
}

func (c *Client) handleServerRequest(msg Message) {
	var result any
	var rpcErr *ResponseError
	c.mu.Lock()
	handler := c.serverRequest
	c.mu.Unlock()
	if handler != nil {
		var err error
		result, err = handler(context.Background(), msg.Method, msg.Params)
		if err != nil {
			rpcErr = &ResponseError{Code: -32603, Message: err.Error()}
		}
	} else if msg.Method == "workspace/configuration" {
		result = []any{}
	}
	response := Message{JSONRPC: "2.0", ID: msg.ID, Error: rpcErr}
	if rpcErr == nil {
		response.Result, _ = json.Marshal(result)
	}
	_ = c.write(response)
}

func (c *Client) failPending(err error) {
	c.mu.Lock()
	c.readErr = err
	c.closed = true
	pending := c.pending
	c.pending = make(map[int]chan *ResponseMessage)
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- &ResponseMessage{Error: &ResponseError{Code: -32000, Message: err.Error()}}
	}
}

func IsUnavailableError(err error) bool { return errors.Is(err, ErrUnavailable) }
