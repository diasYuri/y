package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/diasYuri/y/pkg/policy"
	"github.com/diasYuri/y/pkg/search"
)

type searchProviderFunc struct {
	caps   search.Capabilities
	search func(context.Context, search.Request) (search.Response, error)
}

func (p searchProviderFunc) Capabilities() search.Capabilities { return p.caps }
func (p searchProviderFunc) Search(ctx context.Context, req search.Request) (search.Response, error) {
	return p.search(ctx, req)
}

func TestRegisterWebSearchProvider_UsesRegistryPolicyAndCitationContract(t *testing.T) {
	called := false
	provider := searchProviderFunc{
		caps: search.Capabilities{AllowedDomains: true, Location: true, ContextSize: true, MaxResults: true},
		search: func(_ context.Context, req search.Request) (search.Response, error) {
			called = true
			if req.Query != "y sdk" || len(req.AllowedDomains) != 1 || req.AllowedDomains[0] != "example.com" || req.ContextSize != search.ContextSizeMedium {
				t.Fatalf("unexpected request: %#v", req)
			}
			return search.Response{Results: []search.Result{{
				Title: "Y SDK", URL: "https://example.com/y", Snippet: "SDK documentation", Metadata: map[string]string{"source": "docs"},
			}}}, nil
		},
	}
	registry := NewRegistry(WithPolicy(policy.NewEngine(policy.Config{DisableSensitiveApproval: true})))
	registration, err := RegisterWebSearchProvider(registry, provider)
	if err != nil {
		t.Fatal(err)
	}

	descriptors := registry.List()
	if len(descriptors) != 1 || descriptors[0].Name != WebSearchToolName || !descriptors[0].Sensitive || len(descriptors[0].Capabilities) != 1 || descriptors[0].Capabilities[0] != CapabilityWebSearch {
		t.Fatalf("unexpected descriptor: %#v", descriptors)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(descriptors[0].InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Properties["allowed_domains"]; !ok {
		t.Fatal("supported domain constraint missing from tool schema")
	}
	if _, ok := schema.Properties["context_size"]; !ok {
		t.Fatal("supported result context size missing from tool schema")
	}
	args := json.RawMessage(`{"query":"y sdk","allowed_domains":["example.com"],"context_size":"medium"}`)
	response, err := registry.Handle(context.Background(), ToolRequest{Name: WebSearchToolName, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !called || !strings.Contains(response.Content[0].Text, "https://example.com/y") {
		t.Fatalf("search result not exposed as citation: %#v", response)
	}
	var result search.Response
	if err := json.Unmarshal(response.Details, &result); err != nil || len(result.Results) != 1 || result.Results[0].Metadata["source"] != "docs" {
		t.Fatalf("unexpected structured result: %#v, err=%v", result, err)
	}
	if err := registration.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(registry.List()) != 0 {
		t.Fatal("closing registration did not remove the search tool")
	}
}

func TestNewWebSearchTool_ValidatesInputAndDoesNotAdvertiseUnsupportedControls(t *testing.T) {
	provider := searchProviderFunc{search: func(context.Context, search.Request) (search.Response, error) {
		t.Fatal("provider called for invalid request")
		return search.Response{}, nil
	}}
	desc, handler, err := NewWebSearchTool(provider)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(desc.InputSchema), "allowed_domains") || strings.Contains(string(desc.InputSchema), "location") || strings.Contains(string(desc.InputSchema), "context_size") {
		t.Fatalf("unsupported controls advertised: %s", desc.InputSchema)
	}
	for _, args := range []string{
		`{"query":"  "}`,
		`{"query":"valid","allowed_domains":["example.com"]}`,
		`{"query":"valid","allowed_domains":["https://example.com/path"]}`,
		`{"query":"valid","location":"there"}`,
		`{"query":"valid","context_size":"medium"}`,
	} {
		_, err := handler.Handle(context.Background(), ToolRequest{Arguments: json.RawMessage(args)})
		if err == nil {
			t.Errorf("expected validation failure for %s", args)
		}
	}
	_, err = handler.Handle(context.Background(), ToolRequest{Arguments: json.RawMessage(`{"query":"valid","context_size":"medium"}`)})
	if !errors.Is(err, search.ErrUnsupportedControl) {
		t.Fatalf("unsupported control error = %v", err)
	}

	_, handler, err = NewWebSearchTool(searchProviderFunc{caps: search.Capabilities{ContextSize: true}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handler.Handle(context.Background(), ToolRequest{Arguments: json.RawMessage(`{"query":"valid","context_size":"arbitrary prompt text"}`)})
	if !errors.Is(err, search.ErrInvalidRequest) {
		t.Fatalf("invalid context size error = %v", err)
	}
}

func TestNewWebSearchTool_RejectsUnciteableBackendResults(t *testing.T) {
	_, handler, err := NewWebSearchTool(searchProviderFunc{search: func(context.Context, search.Request) (search.Response, error) {
		return search.Response{Results: []search.Result{{Title: "Unsafe", URL: "https://user:secret@example.com", Snippet: "snippet"}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handler.Handle(context.Background(), ToolRequest{Arguments: json.RawMessage(`{"query":"valid"}`)})
	if !errors.Is(err, search.ErrInvalidResult) {
		t.Fatalf("invalid result error = %v", err)
	}
}

func TestWebSearchTool_PreservesCancellation(t *testing.T) {
	started := make(chan struct{})
	provider := searchProviderFunc{search: func(ctx context.Context, _ search.Request) (search.Response, error) {
		close(started)
		<-ctx.Done()
		return search.Response{}, ctx.Err()
	}}
	registry := NewRegistry(WithPolicy(policy.NewEngine(policy.Config{DisableSensitiveApproval: true})))
	registration, err := RegisterWebSearchProvider(registry, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := registry.Handle(ctx, ToolRequest{Name: WebSearchToolName, Arguments: json.RawMessage(`{"query":"cancel me"}`)})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not preserved: %v", err)
	}
}
