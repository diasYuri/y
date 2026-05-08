package openai_compatible

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

func TestProviderStreamsTextUsageToolAndStop(t *testing.T) {
	var gotRequest chatRequest
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want bearer test-key", got)
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
			`{"choices":[{"index":0,"delta":{"content":"hel"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"lo","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\""}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"README.md\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6,"prompt_tokens_details":{"cached_tokens":1}}}`,
		), nil
	})

	provider := New(WithBaseURL("http://example.invalid"), WithAPIKey("test-key"), WithHTTPClient(client))
	stream, err := provider.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "compat-test", BaseURL: "http://example.invalid"},
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
	if delta := deltaEvent.(ai.ToolCallEvent); delta.Complete || string(delta.ArgumentsDelta) != `{"path"` {
		t.Fatalf("tool delta = %#v, want partial args", delta)
	}
	secondDeltaEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("second tool delta Next returned error: %v", err)
	}
	if delta := secondDeltaEvent.(ai.ToolCallEvent); delta.Complete || string(delta.ArgumentsDelta) != `:"README.md"}` {
		t.Fatalf("second tool delta = %#v, want remaining partial args", delta)
	}
	doneEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("tool done Next returned error: %v", err)
	}
	done := doneEvent.(ai.ToolCallEvent)
	if !done.Complete || done.ToolCall.ID != "call_1" || done.ToolCall.Name != "read_file" || string(done.ToolCall.Arguments) != `{"path":"README.md"}` {
		t.Fatalf("tool done = %#v, want complete tool call", done)
	}
	usageEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("usage Next returned error: %v", err)
	}
	usage := usageEvent.(ai.UsageEvent).Usage
	if usage.InputTokens != 3 || usage.CacheReadTokens != 1 || usage.OutputTokens != 2 || usage.TotalTokens != 6 {
		t.Fatalf("usage = %#v, want normalized compatible usage", usage)
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

	if gotRequest.Model != "compat-test" || !gotRequest.Stream || gotRequest.StreamOptions == nil || !gotRequest.StreamOptions.IncludeUsage {
		t.Fatalf("request basics = %#v, want streaming chat completion with usage", gotRequest)
	}
	if len(gotRequest.Messages) != 2 || gotRequest.Messages[0].Role != "system" || gotRequest.Messages[1].Role != "user" {
		t.Fatalf("messages = %#v, want system and user", gotRequest.Messages)
	}
	if len(gotRequest.Tools) != 1 || gotRequest.Tools[0].Function.Name != "read_file" {
		t.Fatalf("tools = %#v, want read_file", gotRequest.Tools)
	}
}

func TestProviderUsesEnvAPIKey(t *testing.T) {
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer env-key" {
			t.Fatalf("Authorization = %q, want env key", got)
		}
		return sseResponse(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`), nil
	})

	provider := New(
		WithBaseURL("http://example.invalid"),
		WithHTTPClient(client),
		WithEnvLookup(func(name string) string {
			if name == "OPENAI_COMPATIBLE_API_KEY" {
				return "env-key"
			}
			return ""
		}),
	)
	stream, err := provider.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "compat-test", BaseURL: "http://example.invalid"},
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

func TestProviderRequiresAPIKeyUnlessAllowed(t *testing.T) {
	provider := New(WithEnvLookup(func(string) string { return "" }))
	_, err := provider.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "compat-test"},
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
