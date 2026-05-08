package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

func TestProviderStreamsTextUsageAndStop(t *testing.T) {
	var gotRequest responseRequest
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %q, want /responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want bearer test key", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if strings.Contains(string(body), "test-key") {
			t.Fatal("request body leaked API key")
		}
		if err := json.Unmarshal(body, &gotRequest); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		return sseResponse(
			`{"type":"response.created","response":{"id":"resp_1"}}`,
			`{"type":"response.output_text.delta","content_index":0,"delta":"hel"}`,
			`{"type":"response.output_text.delta","content_index":0,"delta":"lo"}`,
			`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5,"input_tokens_details":{"cached_tokens":1}}}}`,
		), nil
	})

	provider := New(
		WithBaseURL("http://example.invalid"),
		WithAPIKey("test-key"),
		WithHTTPClient(client),
		WithEnvLookup(func(string) string { return "" }),
	)
	stream, err := provider.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "gpt-test", BaseURL: "http://example.invalid"},
		Context: ai.Context{
			SystemPrompt: "You are terse.",
			Messages: []ai.Message{{
				Role:    ai.RoleUser,
				Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hello"}},
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
	usageEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("usage Next returned error: %v", err)
	}
	usage := usageEvent.(ai.UsageEvent).Usage
	if usage.InputTokens != 2 || usage.CacheReadTokens != 1 || usage.OutputTokens != 2 || usage.TotalTokens != 5 {
		t.Fatalf("usage = %#v, want normalized cached input usage", usage)
	}
	stopEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("stop Next returned error: %v", err)
	}
	if got := stopEvent.(ai.StopEvent).Reason; got != ai.StopReasonStop {
		t.Fatalf("stop reason = %q, want stop", got)
	}
	if _, err := stream.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Next after stream returned %v, want io.EOF", err)
	}

	if !gotRequest.Stream || gotRequest.Store {
		t.Fatalf("request stream/store = %v/%v, want true/false", gotRequest.Stream, gotRequest.Store)
	}
	if gotRequest.Model != "gpt-test" {
		t.Fatalf("request model = %q, want gpt-test", gotRequest.Model)
	}
	if len(gotRequest.Input) != 2 || gotRequest.Input[0].Role != "system" || gotRequest.Input[1].Role != "user" {
		t.Fatalf("request input = %#v, want system and user messages", gotRequest.Input)
	}
}

func TestProviderStreamsToolCallWithoutAccumulatingText(t *testing.T) {
	client := newMockClient(func(_ *http.Request) (*http.Response, error) {
		return sseResponse(
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":""}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{\"path\""}`,
			`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_1","name":"read_file","arguments":"{\"path\":\"README.md\"}"}`,
			`{"type":"response.completed","response":{"status":"completed"}}`,
		), nil
	})

	provider := New(WithBaseURL("http://example.invalid"), WithAPIKey("test-key"), WithHTTPClient(client))
	stream, err := provider.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "gpt-test", BaseURL: "http://example.invalid"},
		Context: ai.Context{
			Messages: []ai.Message{{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "read"}}}},
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

	deltaEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("tool delta Next returned error: %v", err)
	}
	delta := deltaEvent.(ai.ToolCallEvent)
	if delta.Complete {
		t.Fatal("delta event marked complete")
	}
	if got := string(delta.ArgumentsDelta); got != `{"path"` {
		t.Fatalf("arguments delta = %q, want partial JSON", got)
	}

	doneEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("tool done Next returned error: %v", err)
	}
	done := doneEvent.(ai.ToolCallEvent)
	if !done.Complete {
		t.Fatal("done event was not marked complete")
	}
	if done.ToolCall.ID != "call_1|fc_1" || done.ToolCall.Name != "read_file" {
		t.Fatalf("tool call = %#v, want normalized id and name", done.ToolCall)
	}
	if string(done.ToolCall.Arguments) != `{"path":"README.md"}` {
		t.Fatalf("tool call args = %s, want final JSON", done.ToolCall.Arguments)
	}

	stopEvent, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("stop Next returned error: %v", err)
	}
	if got := stopEvent.(ai.StopEvent).Reason; got != ai.StopReasonToolUse {
		t.Fatalf("stop reason = %q, want tool_use", got)
	}
}

func TestProviderUsesEnvAPIKey(t *testing.T) {
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer env-key" {
			t.Fatalf("Authorization = %q, want env key", got)
		}
		return sseResponse(`{"type":"response.completed","response":{"status":"completed"}}`), nil
	})

	provider := New(
		WithBaseURL("http://example.invalid"),
		WithHTTPClient(client),
		WithEnvLookup(func(name string) string {
			if name == "OPENAI_API_KEY" {
				return "env-key"
			}
			return ""
		}),
	)
	stream, err := provider.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "gpt-test", BaseURL: "http://example.invalid"},
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
		Model: ai.Model{ID: "gpt-test"},
		Context: ai.Context{Messages: []ai.Message{{
			Role:    ai.RoleUser,
			Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hello"}},
		}}},
	})
	if err == nil || !strings.Contains(err.Error(), "API key is required") {
		t.Fatalf("Stream error = %v, want missing API key error", err)
	}
}

func TestStreamNextHonorsContextWhileHTTPBodyIsOpen(t *testing.T) {
	ready := make(chan struct{})
	client := newMockClient(func(_ *http.Request) (*http.Response, error) {
		return blockingSSEResponse(ready), nil
	})

	provider := New(WithBaseURL("http://example.invalid"), WithAPIKey("test-key"), WithHTTPClient(client))
	stream, err := provider.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "gpt-test", BaseURL: "http://example.invalid"},
		Context: ai.Context{Messages: []ai.Message{{
			Role:    ai.RoleUser,
			Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hello"}},
		}}},
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer stream.Close()
	<-ready

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := stream.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next returned %v, want context deadline exceeded", err)
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

type blockingBody struct {
	ready chan struct{}
	done  chan struct{}
	once  sync.Once
}

func (b *blockingBody) Read(_ []byte) (int, error) {
	select {
	case <-b.ready:
	default:
		close(b.ready)
	}
	<-b.done
	return 0, io.EOF
}

func (b *blockingBody) Close() error {
	b.once.Do(func() { close(b.done) })
	return nil
}

func blockingSSEResponse(ready chan struct{}) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       &blockingBody{ready: ready, done: make(chan struct{})},
	}
}
