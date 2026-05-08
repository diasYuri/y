package openai

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

func TestStreamMapsAuthError(t *testing.T) {
	client := newMockClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: http.NoBody}, nil
	})
	p := New(WithBaseURL("http://example.invalid"), WithAPIKey("test-key"), WithHTTPClient(client))
	defer p.Close()
	_, err := p.Stream(context.Background(), providers.StreamRequest{
		Model:   ai.Model{ID: "gpt-test", BaseURL: "http://example.invalid"},
		Context: ai.Context{Messages: []ai.Message{{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hi"}}}}},
	})
	var auth *providers.AuthError
	if !errors.As(err, &auth) {
		t.Fatalf("err = %v, want AuthError", err)
	}
}

func TestDryRunSyntheticStream(t *testing.T) {
	called := 0
	client := newMockClient(func(*http.Request) (*http.Response, error) {
		called++
		return sseResponse(`{"type":"response.completed","response":{"status":"completed"}}`), nil
	})
	p := New(WithBaseURL("http://example.invalid"), WithAPIKey("test-key"), WithHTTPClient(client), WithDryRun())
	defer p.Close()
	stream, err := p.Stream(context.Background(), providers.StreamRequest{
		Model:   ai.Model{ID: "gpt-test", BaseURL: "http://example.invalid"},
		Context: ai.Context{Messages: []ai.Message{{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hi"}}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	if called != 0 {
		t.Fatalf("dry-run sent %d requests, want 0", called)
	}
}

func TestCapabilities(t *testing.T) {
	p := New()
	defer p.Close()
	c := p.Capabilities("gpt-5")
	if !c.Tools || !c.Vision || !c.Reasoning {
		t.Fatalf("caps = %+v", c)
	}
}

func TestNewCompatibleReturnsCompatibleProvider(t *testing.T) {
	p := NewCompatible("http://localhost:11434/v1")
	defer p.Close()
	if p.ID() != "openai-compatible" {
		t.Fatalf("ID() = %q, want openai-compatible", p.ID())
	}
}
