package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"

	extensionhost "github.com/diasYuri/y/pkg/extensions"
	providerauth "github.com/diasYuri/y/pkg/providers/auth"
	"github.com/diasYuri/y/pkg/tools"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Transport identifies the transport used to connect to an MCP server.
type Transport string

const (
	// TransportStdio starts a local MCP server process and communicates over
	// its standard input and output streams.
	TransportStdio Transport = "stdio"
	// TransportStreamableHTTP connects to a remote MCP Streamable HTTP endpoint.
	TransportStreamableHTTP Transport = "streamable_http"
)

// ServerConfig configures one MCP server. Exactly the fields for the selected
// transport must be supplied. AccessToken and TokenSource are only used for
// Streamable HTTP. A TokenSource is resolved for every outgoing request with
// the server name as its provider ID, allowing a host or cloud secret manager
// to supply a rotated bearer token.
type ServerConfig struct {
	Name      string    `json:"name"`
	Transport Transport `json:"transport"`

	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// Env replaces the child process environment when non-nil. A nil map
	// inherits the parent process environment.
	Env map[string]string `json:"-"`

	Endpoint    string              `json:"endpoint,omitempty"`
	Headers     map[string]string   `json:"-"`
	AccessToken string              `json:"-"`
	TokenSource providerauth.Source `json:"-"`
}

// Config configures the MCP servers connected by an Extension.
type Config struct {
	Servers []ServerConfig `json:"servers"`
}

// ServerFailure reports a configured server that could not connect or whose
// initial tool list could not be loaded. Other servers remain available.
type ServerFailure struct {
	Name string
	Err  error
}

type remoteTool struct {
	descriptor tools.ToolDescriptor
	name       string
}

type serverSession struct {
	name       string
	session    *mcpsdk.ClientSession
	tools      []remoteTool
	httpClient *http.Client
}

// Extension connects configured MCP servers and adapts their capabilities to
// the native y extension and tool contracts.
type Extension struct {
	mu          sync.RWMutex
	servers     map[string]*serverSession
	serverOrder []string
	failures    []ServerFailure
	closed      bool
	closeOnce   sync.Once
	closeDone   chan struct{}
	closeErr    error
}

// New validates config, connects to each configured server, and snapshots its
// tools. Individual connection failures are available from Failures and do
// not prevent other servers from connecting. The caller should pass a context
// with a deadline when startup must be bounded.
func New(ctx context.Context, config Config) (*Extension, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	ext := &Extension{
		servers:   make(map[string]*serverSession, len(config.Servers)),
		closeDone: make(chan struct{}),
	}
	for _, serverConfig := range config.Servers {
		if err := ctx.Err(); err != nil {
			_ = ext.Close(context.Background())
			return nil, err
		}
		server, err := connectServer(ctx, serverConfig)
		if err != nil {
			if ctx.Err() != nil {
				_ = ext.Close(context.Background())
				return nil, ctx.Err()
			}
			ext.failures = append(ext.failures, ServerFailure{Name: serverConfig.Name, Err: err})
			continue
		}
		ext.servers[server.name] = server
		ext.serverOrder = append(ext.serverOrder, server.name)
	}
	return ext, nil
}

func validateConfig(config Config) error {
	if len(config.Servers) == 0 {
		return errors.New("mcp: at least one server is required")
	}
	names := make(map[string]struct{}, len(config.Servers))
	for i, server := range config.Servers {
		name := strings.TrimSpace(server.Name)
		if name == "" {
			return fmt.Errorf("mcp: server %d has an empty name", i)
		}
		if name != server.Name {
			return fmt.Errorf("mcp: server name %q has surrounding whitespace", server.Name)
		}
		if _, exists := names[name]; exists {
			return fmt.Errorf("mcp: duplicate server name %q", name)
		}
		names[name] = struct{}{}

		switch server.Transport {
		case TransportStdio:
			if strings.TrimSpace(server.Command) == "" {
				return fmt.Errorf("mcp: stdio server %q requires a command", name)
			}
			if server.Endpoint != "" || len(server.Headers) > 0 || server.AccessToken != "" || server.TokenSource != nil {
				return fmt.Errorf("mcp: stdio server %q cannot configure HTTP fields", name)
			}
		case TransportStreamableHTTP:
			if server.Command != "" || len(server.Args) > 0 || len(server.Env) > 0 {
				return fmt.Errorf("mcp: HTTP server %q cannot configure stdio fields", name)
			}
			parsed, err := url.ParseRequestURI(server.Endpoint)
			if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return fmt.Errorf("mcp: HTTP server %q requires an absolute http or https endpoint", name)
			}
			if server.AccessToken != "" && server.TokenSource != nil {
				return fmt.Errorf("mcp: HTTP server %q cannot set both access token and token source", name)
			}
			if _, ok := headerValue(server.Headers, "Authorization"); ok && (server.AccessToken != "" || server.TokenSource != nil) {
				return fmt.Errorf("mcp: HTTP server %q cannot set Authorization in headers and configure a token", name)
			}
		default:
			return fmt.Errorf("mcp: server %q has unsupported transport %q", name, server.Transport)
		}
	}
	return nil
}

func connectServer(ctx context.Context, config ServerConfig) (*serverSession, error) {
	var transport mcpsdk.Transport
	var httpClient *http.Client
	switch config.Transport {
	case TransportStdio:
		command := exec.Command(config.Command, config.Args...)
		if config.Env != nil {
			command.Env = make([]string, 0, len(config.Env))
			for key, value := range config.Env {
				command.Env = append(command.Env, key+"="+value)
			}
		}
		transport = &mcpsdk.CommandTransport{Command: command}
	case TransportStreamableHTTP:
		var err error
		transport, httpClient, err = httpTransport(config)
		if err != nil {
			return nil, err
		}
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "y", Version: "1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		if httpClient != nil {
			httpClient.CloseIdleConnections()
		}
		return nil, fmt.Errorf("connect: %w", err)
	}
	server := &serverSession{name: config.Name, session: session, httpClient: httpClient}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			_ = session.Close()
			if httpClient != nil {
				httpClient.CloseIdleConnections()
			}
			return nil, fmt.Errorf("list tools: %w", err)
		}
		if tool == nil || strings.TrimSpace(tool.Name) == "" {
			_ = session.Close()
			if httpClient != nil {
				httpClient.CloseIdleConnections()
			}
			return nil, errors.New("list tools: server returned a tool with an empty name")
		}
		schema, err := toolSchema(tool.InputSchema)
		if err != nil {
			_ = session.Close()
			if httpClient != nil {
				httpClient.CloseIdleConnections()
			}
			return nil, fmt.Errorf("list tools: tool %q has invalid input schema: %w", tool.Name, err)
		}
		server.tools = append(server.tools, remoteTool{
			descriptor: tools.ToolDescriptor{
				Name:        "mcp." + config.Name + "." + tool.Name,
				Description: tool.Description,
				InputSchema: schema,
			},
			name: tool.Name,
		})
	}
	return server, nil
}

func toolSchema(value any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage(`{"type":"object","properties":{}}`), nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	if schema == nil {
		return nil, errors.New("schema must be a JSON object")
	}
	return raw, nil
}

func httpTransport(config ServerConfig) (mcpsdk.Transport, *http.Client, error) {
	base := http.DefaultTransport
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		base = defaultTransport.Clone()
	}
	wrapped := &headerTransport{
		base:    base,
		headers: make(http.Header),
		token:   config.AccessToken,
		source:  config.TokenSource,
		server:  config.Name,
	}
	for key, value := range config.Headers {
		wrapped.headers.Set(key, value)
	}
	endpoint, _ := url.Parse(config.Endpoint)
	client := &http.Client{
		Transport: wrapped,
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			if !sameOrigin(endpoint, request.URL) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:             config.Endpoint,
		HTTPClient:           client,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
	return transport, client, nil
}

func sameOrigin(left, right *url.URL) bool {
	return left != nil && right != nil &&
		strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Host, right.Host)
}

type headerTransport struct {
	base    http.RoundTripper
	headers http.Header
	token   string
	source  providerauth.Source
	server  string
}

func (t *headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	for key, values := range t.headers {
		if len(values) > 0 {
			clone.Header.Set(key, values[0])
		}
	}
	token := t.token
	if t.source != nil {
		var err error
		token, err = t.source.Resolve(request.Context(), t.server)
		if err != nil {
			return nil, fmt.Errorf("resolve MCP token for %q: %w", t.server, err)
		}
	}
	if token = strings.TrimSpace(token); token != "" {
		clone.Header.Set("Authorization", bearerValue(token))
	}
	return t.base.RoundTrip(clone)
}

func (t *headerTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func bearerValue(token string) string {
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		return token
	}
	return "Bearer " + token
}

func headerValue(headers map[string]string, name string) (string, bool) {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
}

// ID identifies this extension to a generic host.
func (e *Extension) ID() string { return "mcp" }

// Install registers connected server capabilities on the host's native tool
// registry. Names discovered from each server are fixed until the extension and
// registry are rebuilt.
func (e *Extension) Install(host *extensionhost.Host) error {
	if e == nil || host == nil {
		return errors.New("mcp: extension and host are required")
	}
	return e.RegisterTools(host.Registry)
}

// RegisterTools adds MCP tools, resource operations, and prompt operations to
// registry. It checks all names before adding any entry so ordinary duplicate
// names fail without partially registering this extension.
func (e *Extension) RegisterTools(registry *tools.Registry) error {
	if e == nil || registry == nil {
		return errors.New("mcp: extension and registry are required")
	}
	e.mu.RLock()
	if e.closed {
		e.mu.RUnlock()
		return errors.New("mcp: extension is closed")
	}
	servers := make([]*serverSession, 0, len(e.serverOrder))
	for _, name := range e.serverOrder {
		servers = append(servers, e.servers[name])
	}
	e.mu.RUnlock()
	return registerTools(registry, servers)
}

// Failures returns connection and discovery failures in configured server
// order. The returned slice is independent of the extension.
func (e *Extension) Failures() []ServerFailure {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]ServerFailure(nil), e.failures...)
}

// Close stops all MCP sessions and closes their idle HTTP connections. It is
// safe to call more than once. If ctx expires while sessions are draining, the
// close continues in the background and a later call can wait for completion.
func (e *Extension) Close(ctx context.Context) error {
	if e == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		servers := make([]*serverSession, 0, len(e.serverOrder))
		for _, name := range e.serverOrder {
			servers = append(servers, e.servers[name])
		}
		e.mu.Unlock()
		go func() {
			var wg sync.WaitGroup
			var errMu sync.Mutex
			for _, server := range servers {
				server := server
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := server.session.Close(); err != nil {
						errMu.Lock()
						e.closeErr = errors.Join(e.closeErr, fmt.Errorf("close MCP server %q: %w", server.name, err))
						errMu.Unlock()
					}
					if server.httpClient != nil {
						server.httpClient.CloseIdleConnections()
					}
				}()
			}
			wg.Wait()
			close(e.closeDone)
		}()
	})
	select {
	case <-e.closeDone:
		return e.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
