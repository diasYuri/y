package anthropic

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

func TestStreamMapsAuthError(t *testing.T) {
	client := newMockClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{},
			Body:       http.NoBody,
		}, nil
	})
	p := New(WithBaseURL("http://example.invalid"), WithAPIKey("test-key"), WithHTTPClient(client))
	defer p.Close()

	_, err := p.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "claude-test", BaseURL: "http://example.invalid"},
		Context: ai.Context{Messages: []ai.Message{{
			Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hi"}},
		}}},
	})
	var authErr *providers.AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %v, want AuthError", err)
	}
	if authErr.StatusCode != 401 {
		t.Fatalf("AuthError.StatusCode = %d, want 401", authErr.StatusCode)
	}
}

func TestStreamMapsRateLimitError(t *testing.T) {
	client := newMockClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"7"}},
			Body:       http.NoBody,
		}, nil
	})
	p := New(WithBaseURL("http://example.invalid"), WithAPIKey("test-key"), WithHTTPClient(client))
	defer p.Close()

	_, err := p.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "claude-test", BaseURL: "http://example.invalid"},
		Context: ai.Context{Messages: []ai.Message{{
			Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hi"}},
		}}},
	})
	var rl *providers.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want RateLimitError", err)
	}
	if rl.RetryAfter == 0 {
		t.Fatalf("RetryAfter = 0; want >0")
	}
}

func TestRequestInspectorSeesRequest(t *testing.T) {
	var captured *http.Request
	client := newMockClient(func(*http.Request) (*http.Response, error) {
		return sseResponse(`{"type":"message_stop"}`), nil
	})
	p := New(
		WithBaseURL("http://example.invalid"),
		WithAPIKey("test-key"),
		WithHTTPClient(client),
		WithRequestInspector(func(r *http.Request) { captured = r }),
	)
	defer p.Close()

	stream, err := p.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "claude-test", BaseURL: "http://example.invalid"},
		Context: ai.Context{Messages: []ai.Message{{
			Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hi"}},
		}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	if captured == nil {
		t.Fatal("inspector not invoked")
	}
	if !strings.HasSuffix(captured.URL.Path, "/v1/messages") {
		t.Fatalf("inspector saw URL %s, want /v1/messages suffix", captured.URL.Path)
	}
	if captured.Header.Get("X-API-Key") != "test-key" {
		t.Fatalf("inspector header X-API-Key = %q, want test-key", captured.Header.Get("X-API-Key"))
	}
}

func TestDryRunDoesNotSend(t *testing.T) {
	requestCount := 0
	client := newMockClient(func(*http.Request) (*http.Response, error) {
		requestCount++
		return sseResponse(`{"type":"message_stop"}`), nil
	})
	var inspected bool
	p := New(
		WithBaseURL("http://example.invalid"),
		WithAPIKey("test-key"),
		WithHTTPClient(client),
		WithRequestInspector(func(*http.Request) { inspected = true }),
		WithDryRun(),
	)
	defer p.Close()

	stream, err := p.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "claude-test", BaseURL: "http://example.invalid"},
		Context: ai.Context{Messages: []ai.Message{{
			Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hi"}},
		}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	if !inspected {
		t.Fatal("inspector not invoked in dry-run")
	}
	if requestCount != 0 {
		t.Fatalf("dry-run sent %d requests, want 0", requestCount)
	}
	ev, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.Kind() != ai.EventStop {
		t.Fatalf("dry-run synthetic stream emitted %q, want stop", ev.Kind())
	}
}

func TestMiddlewareWrapsTransport(t *testing.T) {
	wrapped := 0
	client := newMockClient(func(*http.Request) (*http.Response, error) {
		return sseResponse(`{"type":"message_stop"}`), nil
	})
	mw := func(next http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			wrapped++
			return next.RoundTrip(r)
		})
	}
	p := New(
		WithBaseURL("http://example.invalid"),
		WithAPIKey("test-key"),
		WithHTTPClient(client),
		WithMiddleware(mw),
	)
	defer p.Close()

	stream, err := p.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "claude-test", BaseURL: "http://example.invalid"},
		Context: ai.Context{Messages: []ai.Message{{
			Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hi"}},
		}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	if wrapped == 0 {
		t.Fatal("middleware not invoked")
	}
}

func TestPerRequestAPIKeyOverridesConstructor(t *testing.T) {
	var seen string
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		seen = r.Header.Get("X-API-Key")
		return sseResponse(`{"type":"message_stop"}`), nil
	})
	p := New(
		WithBaseURL("http://example.invalid"),
		WithAPIKey("constructor-key"),
		WithHTTPClient(client),
	)
	defer p.Close()

	stream, err := p.Stream(context.Background(), providers.StreamRequest{
		Model: ai.Model{ID: "claude-test", BaseURL: "http://example.invalid"},
		Context: ai.Context{Messages: []ai.Message{{
			Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hi"}},
		}}},
		Options: providers.StreamOptions{APIKey: "request-key"},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	if seen != "request-key" {
		t.Fatalf("X-API-Key = %q, want per-request override 'request-key'", seen)
	}
}

func TestCapabilitiesReportsClaudeFamily(t *testing.T) {
	p := New()
	defer p.Close()
	c := p.Capabilities("claude-sonnet-4-5")
	if !c.Vision || !c.Tools || !c.Reasoning || !c.PromptCache {
		t.Fatalf("caps = %+v, want full Claude family caps", c)
	}
	if got := p.Capabilities(""); got != (providers.Capabilities{}) {
		t.Fatalf("empty modelID caps = %+v, want zero", got)
	}
}

func TestCountTokensFallsBackToEstimate(t *testing.T) {
	p := New(WithEnvLookup(func(string) string { return "" }))
	defer p.Close()
	got, err := p.CountTokens(context.Background(), "claude-sonnet-4-5", ai.Context{
		Messages: []ai.Message{{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "estimate me please"}}}},
	})
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if got <= 0 {
		t.Fatalf("CountTokens = %d, want > 0", got)
	}
}
