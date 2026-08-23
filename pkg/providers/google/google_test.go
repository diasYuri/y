package google

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
	var gotRequest struct {
		SystemInstruction *struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"`
		Contents []struct {
			Role string `json:"role"`
		} `json:"contents"`
		Tools []struct {
			FunctionDeclarations []struct {
				Name string `json:"name"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
	}
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/models/gemini-test:streamGenerateContent" {
			t.Fatalf("path = %q, want Gemini stream path", r.URL.Path)
		}
		if got := r.URL.Query().Get("alt"); got != "sse" {
			t.Fatalf("alt query = %q, want sse", got)
		}
		if got := r.URL.Query().Get("key"); got != "test-key" {
			t.Fatalf("key query = %q, want test-key", got)
		}
		if got := r.Header.Get("X-Goog-Api-Key"); got != "test-key" {
			t.Fatalf("X-Goog-Api-Key = %q, want test-key", got)
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
			`{"candidates":[{"content":{"parts":[{"text":"hel"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"lo"},{"functionCall":{"id":"call_1","name":"read_file","args":{"path":"README.md"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"thoughtsTokenCount":1,"cachedContentTokenCount":1,"totalTokenCount":8}}`,
		), nil
	})

	provider := New(WithBaseURL("http://example.invalid"), WithAPIKey("test-key"), WithHTTPClient(client))
	stream, err := provider.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "gemini-test", BaseURL: "http://example.invalid"},
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
	toolEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("tool Next returned error: %v", err)
	}
	tool := toolEvent.(ai.ToolCallEvent)
	if !tool.Complete || tool.ToolCall.ID != "call_1" || tool.ToolCall.Name != "read_file" || string(tool.ToolCall.Arguments) != `{"path":"README.md"}` {
		t.Fatalf("tool event = %#v, want normalized complete tool call", tool)
	}
	stopEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("stop Next returned error: %v", err)
	}
	if got := stopEvent.(ai.StopEvent).Reason; got != ai.StopReasonToolUse {
		t.Fatalf("stop reason = %q, want tool_use", got)
	}
	usageEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("usage Next returned error: %v", err)
	}
	usage := usageEvent.(ai.UsageEvent).Usage
	if usage.InputTokens != 3 || usage.CacheReadTokens != 1 || usage.OutputTokens != 3 || usage.TotalTokens != 8 {
		t.Fatalf("usage = %#v, want normalized google usage", usage)
	}
	if _, err := stream.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Next after stream returned %v, want io.EOF", err)
	}

	if gotRequest.SystemInstruction == nil || gotRequest.SystemInstruction.Parts[0].Text != "You are terse." {
		t.Fatalf("system instruction = %#v, want prompt", gotRequest.SystemInstruction)
	}
	if len(gotRequest.Contents) != 1 || gotRequest.Contents[0].Role != "user" {
		t.Fatalf("contents = %#v, want user content", gotRequest.Contents)
	}
	if len(gotRequest.Tools) != 1 || gotRequest.Tools[0].FunctionDeclarations[0].Name != "read_file" {
		t.Fatalf("tools = %#v, want read_file declaration", gotRequest.Tools)
	}
}

func TestProviderModelsUsesOfficialSDK(t *testing.T) {
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta/models" {
			t.Fatalf("request = %s %s, want GET /v1beta/models", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("key"); got != "test-key" {
			t.Fatalf("key query = %q, want test-key", got)
		}
		if got := r.Header.Get("X-Goog-Api-Key"); got != "test-key" {
			t.Fatalf("X-Goog-Api-Key = %q, want test-key", got)
		}
		return jsonResponse(`{"models":[{"name":"models/gemini-2.5-flash","displayName":"Gemini 2.5 Flash","inputTokenLimit":1000000,"outputTokenLimit":8192}]}`), nil
	})

	p := New(WithBaseURL("http://example.invalid/v1beta"), WithAPIKey("test-key"), WithHTTPClient(client))
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gemini-2.5-flash" {
		t.Fatalf("models = %#v, want official Gemini response", models)
	}
}

func TestCountTokensUsesOfficialSDK(t *testing.T) {
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta/models/gemini-test:countTokens" {
			t.Fatalf("request = %s %s, want POST /v1beta/models/gemini-test:countTokens", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("key"); got != "test-key" {
			t.Fatalf("query key = %q, want test-key", got)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
			t.Fatalf("x-goog-api-key = %q, want test-key", got)
		}
		return jsonResponse(`{"totalTokens":42}`), nil
	})

	p := New(WithBaseURL("http://example.invalid/v1beta"), WithAPIKey("test-key"), WithHTTPClient(client))
	got, err := p.CountTokens(context.Background(), "gemini-test", ai.Context{
		Messages: []ai.Message{{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hello"}}}},
	})
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if got != 42 {
		t.Fatalf("CountTokens = %d, want 42", got)
	}
}

func TestProviderRequiresAPIKey(t *testing.T) {
	provider := New(WithEnvLookup(func(string) string { return "" }))
	_, err := provider.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "gemini-test"},
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

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
