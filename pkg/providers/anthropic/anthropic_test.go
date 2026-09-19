package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/providers"
)

func TestProviderStreamsTextUsageToolAndStop(t *testing.T) {
	var gotRequest struct {
		Model     string          `json:"model"`
		MaxTokens int64           `json:"max_tokens"`
		Stream    bool            `json:"stream"`
		System    json.RawMessage `json:"system"`
		Messages  []struct {
			Role string `json:"role"`
		} `json:"messages"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %q, want /v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("X-API-Key"); got != "test-key" {
			t.Fatalf("X-API-Key = %q, want test-key", got)
		}
		if got := r.Header.Get("Anthropic-Version"); got != anthropicVersion {
			t.Fatalf("Anthropic-Version = %q, want %q", got, anthropicVersion)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if strings.Contains(string(body), "test-key") {
			t.Fatal("request body leaked API key")
		}
		if err := json.Unmarshal(body, &gotRequest); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		return sseResponse(
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\""}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":":\"README.md\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":3,"output_tokens":2,"cache_read_input_tokens":1,"cache_creation_input_tokens":4}}`,
		), nil
	})

	provider := New(WithBaseURL("http://example.invalid"), WithAPIKey("test-key"), WithHTTPClient(client))
	stream, err := provider.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "claude-test", BaseURL: "http://example.invalid"},
		Context: ai.Context{
			SystemPrompt: "You are terse.",
			Messages: []ai.Message{{
				Role:    ai.RoleUser,
				Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hello"}},
			}},
			Tools: []ai.Tool{{
				Name:        "read_file",
				Description: "Read a file",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}},
		},
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer stream.Close()

	first, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("first Next returned error: %v", err)
	}
	if got := first.(ai.TextDelta).Text; got != "hel" {
		t.Fatalf("first text delta = %q, want hel", got)
	}
	second, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("second Next returned error: %v", err)
	}
	if got := second.(ai.TextDelta).Text; got != "lo" {
		t.Fatalf("second text delta = %q, want lo", got)
	}
	deltaEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("tool delta Next returned error: %v", err)
	}
	delta, ok := deltaEvent.(ai.ToolCallEvent)
	if !ok {
		t.Fatalf("tool delta event = %#v, want ToolCallEvent", deltaEvent)
	}
	if delta.Complete || string(delta.ArgumentsDelta) != `{"path"` {
		t.Fatalf("tool delta = %#v, want partial args", delta)
	}
	secondDeltaEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("second tool delta Next returned error: %v", err)
	}
	delta, ok = secondDeltaEvent.(ai.ToolCallEvent)
	if !ok {
		t.Fatalf("second tool delta event = %#v, want ToolCallEvent", secondDeltaEvent)
	}
	if delta.Complete || string(delta.ArgumentsDelta) != `:"README.md"}` {
		t.Fatalf("second tool delta = %#v, want remaining partial args", delta)
	}
	toolEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("tool Next returned error: %v", err)
	}
	tool := toolEvent.(ai.ToolCallEvent)
	if !tool.Complete || tool.ToolCall.ID != "toolu_1" || tool.ToolCall.Name != "read_file" || string(tool.ToolCall.Arguments) != `{"path":"README.md"}` {
		t.Fatalf("tool event = %#v, want normalized complete tool call", tool)
	}
	usageEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("usage Next returned error: %v", err)
	}
	usage := usageEvent.(ai.UsageEvent).Usage
	if usage.InputTokens != 3 || usage.OutputTokens != 2 || usage.CacheReadTokens != 1 || usage.CacheWriteTokens != 4 || usage.TotalTokens != 10 {
		t.Fatalf("usage = %#v, want normalized anthropic usage", usage)
	}
	stopEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("stop Next returned error: %v", err)
	}
	if got := stopEvent.(ai.StopEvent).Reason; got != ai.StopReasonToolUse {
		t.Fatalf("stop reason = %q, want tool_use", got)
	}
	if _, err := stream.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Next after stream returned %v, want io.EOF", err)
	}

	if gotRequest.Model != "claude-test" || !gotRequest.Stream || gotRequest.MaxTokens != defaultMaxTokens {
		t.Fatalf("request basics = %#v, want model, streaming and default max tokens", gotRequest)
	}
	var systemBlocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(gotRequest.System, &systemBlocks); err != nil || len(systemBlocks) != 1 || systemBlocks[0].Text != "You are terse." || len(gotRequest.Messages) != 1 || gotRequest.Messages[0].Role != "user" {
		t.Fatalf("request messages = %#v, want system and user message", gotRequest)
	}
	if len(gotRequest.Tools) != 1 || gotRequest.Tools[0].Name != "read_file" {
		t.Fatalf("request tools = %#v, want read_file", gotRequest.Tools)
	}
}

func TestProviderModelsUsesOfficialSDK(t *testing.T) {
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Fatalf("request = %s %s, want GET /v1/models", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-API-Key"); got != "test-key" {
			t.Fatalf("X-API-Key = %q, want test-key", got)
		}
		if got := r.Header.Get("Anthropic-Version"); got != anthropicVersion {
			t.Fatalf("Anthropic-Version = %q, want %q", got, anthropicVersion)
		}
		return jsonResponse(`{"data":[{"id":"claude-test","display_name":"Claude Test","created_at":"2025-01-01T00:00:00Z","type":"model","max_input_tokens":100000,"max_tokens":4096,"capabilities":{"image_input":{"supported":true},"thinking":{"supported":true},"effort":{"supported":false}}}],"has_more":false,"first_id":"claude-test","last_id":"claude-test"}`), nil
	})

	p := New(WithBaseURL("http://example.invalid"), WithAPIKey("test-key"), WithHTTPClient(client))
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "claude-test" || !models[0].Reasoning {
		t.Fatalf("models = %#v, want official Claude response", models)
	}
}

func TestProviderPreservesExplicitCorrelationHeaders(t *testing.T) {
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("X-Request-ID"); got != "caller-request" {
			t.Fatalf("X-Request-ID = %q, want caller-request", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "caller-idempotency" {
			t.Fatalf("Idempotency-Key = %q, want caller-idempotency", got)
		}
		if got := r.Header.Get("X-Y-Run-ID"); got != "run-1" {
			t.Fatalf("X-Y-Run-ID = %q, want run-1", got)
		}
		if got := r.Header.Get("X-Y-Turn-ID"); got != "turn-1" {
			t.Fatalf("X-Y-Turn-ID = %q, want turn-1", got)
		}
		return sseResponse(`{"type":"message_stop"}`), nil
	})
	provider := New(WithBaseURL("http://example.invalid"), WithAPIKey("test-key"), WithHTTPClient(client))
	stream, err := provider.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "claude-test", BaseURL: "http://example.invalid"},
		Context: ai.Context{Messages: []ai.Message{{
			Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hello"}},
		}}},
		Options: providers.StreamOptions{
			RequestID: "generated-request", IdempotencyKey: "generated-idempotency",
			RunID: "run-1", TurnID: "turn-1",
			Headers: map[string]string{"x-request-id": "caller-request", "IDEMPOTENCY-KEY": "caller-idempotency"},
		},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	for {
		_, err := stream.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
	}
}

func TestBuildSDKMessageRequestTreatsThinkingOffAsDisabled(t *testing.T) {
	payload, err := buildSDKMessageRequest(providers.StreamRequest{
		Model:   ai.Model{ID: "claude-test", Reasoning: true},
		Options: providers.StreamOptions{Reasoning: ai.ThinkingOff},
	})
	if err != nil {
		t.Fatalf("buildSDKMessageRequest: %v", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if strings.Contains(string(raw), `"thinking"`) {
		t.Fatalf("request enabled thinking for ThinkingOff: %s", raw)
	}
}

func TestCountTokensUsesOfficialSDK(t *testing.T) {
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages/count_tokens" {
			t.Fatalf("request = %s %s, want POST /v1/messages/count_tokens", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-API-Key"); got != "test-key" {
			t.Fatalf("X-API-Key = %q, want test-key", got)
		}
		if got := r.Header.Get("Anthropic-Version"); got != anthropicVersion {
			t.Fatalf("Anthropic-Version = %q, want %s", got, anthropicVersion)
		}
		return jsonResponse(`{"input_tokens":42}`), nil
	})

	p := New(WithBaseURL("http://example.invalid"), WithAPIKey("test-key"), WithHTTPClient(client))
	got, err := p.CountTokens(context.Background(), "claude-test", ai.Context{
		SystemPrompt: "Be concise.",
		Messages:     []ai.Message{{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hello"}}}},
	})
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if got != 42 {
		t.Fatalf("CountTokens = %d, want 42", got)
	}
}

func TestProviderUsesOAuthTokenBeforeAPIKey(t *testing.T) {
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer oauth-token" {
			t.Fatalf("Authorization = %q, want OAuth bearer", got)
		}
		if got := r.Header.Get("X-API-Key"); got != "" {
			t.Fatalf("X-API-Key = %q, want empty when OAuth is used", got)
		}
		return sseResponse(`{"type":"message_stop"}`), nil
	})

	provider := New(
		WithBaseURL("http://example.invalid"),
		WithHTTPClient(client),
		WithEnvLookup(func(name string) string {
			switch name {
			case "ANTHROPIC_OAUTH_TOKEN":
				return "oauth-token"
			case "ANTHROPIC_API_KEY":
				return "api-key"
			default:
				return ""
			}
		}),
	)
	stream, err := provider.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "claude-test", BaseURL: "http://example.invalid"},
		Context: ai.Context{Messages: []ai.Message{{
			Role:    ai.RoleUser,
			Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hello"}},
		}}},
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer stream.Close()
}

func TestProviderRequiresAPIKey(t *testing.T) {
	provider := New(WithEnvLookup(func(string) string { return "" }))
	_, err := provider.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "claude-test"},
		Context: ai.Context{Messages: []ai.Message{{
			Role:    ai.RoleUser,
			Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hello"}},
		}}},
	})
	if err == nil || !strings.Contains(err.Error(), "API key is required") {
		t.Fatalf("Stream error = %v, want missing API key error", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newMockClient(fn func(*http.Request) (*http.Response, error)) *http.Client {
	return &http.Client{Transport: roundTripFunc(fn)}
}

func sseResponse(events ...string) *http.Response {
	var body strings.Builder
	for _, event := range events {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(event), &envelope); err == nil && envelope.Type != "" {
			body.WriteString("event: ")
			body.WriteString(envelope.Type)
			body.WriteString("\n")
		}
		body.WriteString("data: ")
		body.WriteString(event)
		body.WriteString("\n\n")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body.String())),
	}
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
