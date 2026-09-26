package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	runtimeextensions "github.com/diasYuri/y/pkg/extensions"
	providerauth "github.com/diasYuri/y/pkg/providers/auth"
	"github.com/diasYuri/y/pkg/tools"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExtensionHTTPToolsResourcesPromptsAndBearerToken(t *testing.T) {
	server := fixtureServer()
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	var requests atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer cloud-token" {
			http.Error(w, "invalid bearer token", http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()

	ext, err := New(context.Background(), Config{Servers: []ServerConfig{{
		Name:        "fixture",
		Transport:   TransportStreamableHTTP,
		Endpoint:    httpServer.URL,
		TokenSource: &providerauth.StaticSource{Key: "cloud-token"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Close(context.Background())
	if failures := ext.Failures(); len(failures) != 0 {
		t.Fatalf("connection failures = %#v, want none", failures)
	}

	registry := tools.NewRegistry()
	host := runtimeextensions.NewHost(registry)
	if err := host.Install(ext); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"mcp.fixture.echo",
		"mcp.fixture.resources.list",
		"mcp.fixture.resources.templates.list",
		"mcp.fixture.resources.read",
		"mcp.fixture.prompts.list",
		"mcp.fixture.prompts.get",
	} {
		if _, _, ok := registry.Get(name); !ok {
			t.Errorf("tool %q was not registered", name)
		}
	}

	echo, err := registry.Handle(context.Background(), tools.ToolRequest{
		Name:      "mcp.fixture.echo",
		Arguments: json.RawMessage(`{"text":"hello"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if echo.IsError || !strings.Contains(responseText(echo), "echo: hello") {
		t.Fatalf("echo response = %#v", echo)
	}
	if !strings.Contains(string(echo.Details), `"value":"hello"`) {
		t.Fatalf("echo details = %s", echo.Details)
	}

	resourceList, err := registry.Handle(context.Background(), tools.ToolRequest{Name: "mcp.fixture.resources.list", Arguments: json.RawMessage(`{}`)})
	if err != nil || !strings.Contains(responseText(resourceList), "test://fixture/data") {
		t.Fatalf("resource list response = %#v, err = %v", resourceList, err)
	}
	resourceTemplates, err := registry.Handle(context.Background(), tools.ToolRequest{Name: "mcp.fixture.resources.templates.list", Arguments: json.RawMessage(`{}`)})
	if err != nil || !strings.Contains(responseText(resourceTemplates), "test://fixture/items/{id}") {
		t.Fatalf("resource template list response = %#v, err = %v", resourceTemplates, err)
	}
	resource, err := registry.Handle(context.Background(), tools.ToolRequest{
		Name:      "mcp.fixture.resources.read",
		Arguments: json.RawMessage(`{"uri":"test://fixture/data"}`),
	})
	if err != nil || !strings.Contains(responseText(resource), "resource body") {
		t.Fatalf("resource read response = %#v, err = %v", resource, err)
	}

	promptList, err := registry.Handle(context.Background(), tools.ToolRequest{Name: "mcp.fixture.prompts.list", Arguments: json.RawMessage(`{}`)})
	if err != nil || !strings.Contains(responseText(promptList), "greet") {
		t.Fatalf("prompt list response = %#v, err = %v", promptList, err)
	}
	prompt, err := registry.Handle(context.Background(), tools.ToolRequest{
		Name:      "mcp.fixture.prompts.get",
		Arguments: json.RawMessage(`{"name":"greet","arguments":{"name":"Y"}}`),
	})
	if err != nil || !strings.Contains(responseText(prompt), "[user] hello Y") {
		t.Fatalf("prompt response = %#v, err = %v", prompt, err)
	}
	if requests.Load() == 0 {
		t.Fatal("MCP server received no authenticated HTTP requests")
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ext.RegisterTools(tools.NewRegistry()); err == nil {
		t.Fatal("closed extension still registered tools")
	}
}

func TestExtensionKeepsConnectedServersWhenAnotherFails(t *testing.T) {
	server := fixtureServer()
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	httpServer := httptest.NewServer(mcpHandler)
	goodEndpoint := httpServer.URL
	httpServer.Close()

	goodServer := httptest.NewServer(mcpHandler)
	defer goodServer.Close()
	ext, err := New(context.Background(), Config{Servers: []ServerConfig{
		{Name: "good", Transport: TransportStreamableHTTP, Endpoint: goodServer.URL},
		{Name: "offline", Transport: TransportStreamableHTTP, Endpoint: goodEndpoint},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Close(context.Background())
	if failures := ext.Failures(); len(failures) != 1 || failures[0].Name != "offline" || failures[0].Err == nil {
		t.Fatalf("failures = %#v, want the offline server", failures)
	}
	registry := tools.NewRegistry()
	if err := ext.RegisterTools(registry); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := registry.Get("mcp.good.echo"); !ok {
		t.Fatal("connected server tools were not registered")
	}
	if _, _, ok := registry.Get("mcp.offline.echo"); ok {
		t.Fatal("failed server tools were registered")
	}
}

func TestHTTPBearerTokenIsNotForwardedAcrossOrigins(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	ext, err := New(context.Background(), Config{Servers: []ServerConfig{{
		Name: "redirect", Transport: TransportStreamableHTTP, Endpoint: redirect.URL, AccessToken: "secret",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Close(context.Background())
	if failures := ext.Failures(); len(failures) != 1 {
		t.Fatalf("failures = %#v, want one rejected redirect", failures)
	}
	if got := targetRequests.Load(); got != 0 {
		t.Fatalf("cross-origin target received %d requests, want none", got)
	}
}

func TestRegisterToolsDetectsCollisionsBeforeAddingTools(t *testing.T) {
	server := fixtureServer()
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	httpServer := httptest.NewServer(mcpHandler)
	defer httpServer.Close()

	ext, err := New(context.Background(), Config{Servers: []ServerConfig{{Name: "fixture", Transport: TransportStreamableHTTP, Endpoint: httpServer.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Close(context.Background())
	registry := tools.NewRegistry()
	if err := registry.Add(tools.ToolDescriptor{Name: "mcp.fixture.echo"}, tools.ToolHandlerFunc(func(context.Context, tools.ToolRequest) (tools.ToolResponse, error) {
		return tools.ToolResponse{}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := ext.RegisterTools(registry); err == nil {
		t.Fatal("RegisterTools succeeded with a name collision")
	}
	if _, _, ok := registry.Get("mcp.fixture.resources.list"); ok {
		t.Fatal("RegisterTools partially changed the registry after collision")
	}
}

func TestExtensionStdioTransport(t *testing.T) {
	ext, err := New(context.Background(), Config{Servers: []ServerConfig{{
		Name:      "stdio",
		Transport: TransportStdio,
		Command:   os.Args[0],
		Args:      []string{"-test.run=^TestMCPStdioHelperProcess$"},
		Env:       map[string]string{"Y_MCP_STDIO_HELPER": "1"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Close(context.Background())
	registry := tools.NewRegistry()
	if err := ext.RegisterTools(registry); err != nil {
		t.Fatal(err)
	}
	response, err := registry.Handle(context.Background(), tools.ToolRequest{
		Name:      "mcp.stdio.echo",
		Arguments: json.RawMessage(`{"text":"stdio"}`),
	})
	if err != nil || !strings.Contains(responseText(response), "echo: stdio") {
		t.Fatalf("stdio response = %#v, err = %v", response, err)
	}
}

func TestMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv("Y_MCP_STDIO_HELPER") != "1" {
		return
	}
	if err := fixtureServer().Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name   string
		config Config
	}{
		{name: "no servers"},
		{name: "duplicate names", config: Config{Servers: []ServerConfig{
			{Name: "same", Transport: TransportStdio, Command: "server"},
			{Name: "same", Transport: TransportStdio, Command: "server"},
		}}},
		{name: "http bearer header collision", config: Config{Servers: []ServerConfig{{
			Name: "http", Transport: TransportStreamableHTTP, Endpoint: "https://example.test/mcp",
			AccessToken: "token", Headers: map[string]string{"authorization": "other"},
		}}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(context.Background(), test.config); err == nil {
				t.Fatal("New succeeded with invalid configuration")
			}
		})
	}
}

func TestConfigJSONOmitsCredentialsAndEnvironment(t *testing.T) {
	config := ServerConfig{
		Name:        "cloud",
		Transport:   TransportStreamableHTTP,
		Endpoint:    "https://mcp.example.test",
		Headers:     map[string]string{"X-API-Key": "header-secret"},
		AccessToken: "oauth-secret",
		TokenSource: &providerauth.StaticSource{Key: "source-secret"},
		Env:         map[string]string{"PRIVATE_KEY": "env-secret"},
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"header-secret", "oauth-secret", "source-secret", "env-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("serialized config contains secret %q: %s", secret, raw)
		}
	}
}

func fixtureServer() *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fixture", Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{
		Name:        "echo",
		Description: "Echo text for tests.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
	}, func(_ context.Context, request *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var args struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &mcpsdk.CallToolResult{
			Content:           []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo: " + args.Text}},
			StructuredContent: map[string]any{"value": args.Text},
		}, nil
	})
	server.AddResource(&mcpsdk.Resource{Name: "fixture data", URI: "test://fixture/data", MIMEType: "text/plain"}, func(_ context.Context, request *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{URI: request.Params.URI, MIMEType: "text/plain", Text: "resource body"}}}, nil
	})
	server.AddResourceTemplate(&mcpsdk.ResourceTemplate{Name: "fixture items", URITemplate: "test://fixture/items/{id}"}, func(_ context.Context, request *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{URI: request.Params.URI, MIMEType: "text/plain", Text: "resource template body"}}}, nil
	})
	server.AddPrompt(&mcpsdk.Prompt{Name: "greet", Arguments: []*mcpsdk.PromptArgument{{Name: "name", Required: true}}}, func(_ context.Context, request *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
		return &mcpsdk.GetPromptResult{Messages: []*mcpsdk.PromptMessage{{Role: "user", Content: &mcpsdk.TextContent{Text: "hello " + request.Params.Arguments["name"]}}}}, nil
	})
	return server
}

func responseText(response tools.ToolResponse) string {
	var builder strings.Builder
	for _, block := range response.Content {
		builder.WriteString(block.Text)
	}
	return builder.String()
}
