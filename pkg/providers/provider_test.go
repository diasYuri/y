package providers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/yuri/y/pkg/ai"
)

func TestEstimateTokensReasonable(t *testing.T) {
	ctx := ai.Context{
		SystemPrompt: "You are a terse assistant.",
		Messages: []ai.Message{{
			Role:    ai.RoleUser,
			Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "Hello there, how are you doing today?"}},
		}},
	}
	got := EstimateTokens(ctx)
	if got <= 0 {
		t.Fatalf("EstimateTokens returned %d, want > 0", got)
	}
	if got > 200 {
		t.Fatalf("EstimateTokens returned %d, want a small estimate (<200)", got)
	}
}

func TestProviderExtrasWithProviderExtra(t *testing.T) {
	extras := WithProviderExtra("foo", 1)
	if got := extras["foo"].(int); got != 1 {
		t.Fatalf("extras[foo] = %v, want 1", got)
	}
	more := extras.WithProviderExtra("bar", "two")
	if more["foo"].(int) != 1 || more["bar"].(string) != "two" {
		t.Fatalf("WithProviderExtra did not preserve+add: %#v", more)
	}
	if _, ok := extras["bar"]; ok {
		t.Fatal("WithProviderExtra mutated original extras")
	}
}

func TestClassifyHTTPErrorMaps(t *testing.T) {
	tests := []struct {
		status int
		want   any
	}{
		{http.StatusTooManyRequests, &RateLimitError{}},
		{http.StatusUnauthorized, &AuthError{}},
		{http.StatusForbidden, &AuthError{}},
		{http.StatusInternalServerError, &NetworkError{}},
		{http.StatusBadGateway, &NetworkError{}},
	}
	for _, tc := range tests {
		err := ClassifyHTTPError("test", tc.status, 0, "boom", nil)
		switch tc.want.(type) {
		case *RateLimitError:
			var rl *RateLimitError
			if !errors.As(err, &rl) {
				t.Fatalf("status %d → %v; want RateLimitError", tc.status, err)
			}
		case *AuthError:
			var a *AuthError
			if !errors.As(err, &a) {
				t.Fatalf("status %d → %v; want AuthError", tc.status, err)
			}
		case *NetworkError:
			var n *NetworkError
			if !errors.As(err, &n) {
				t.Fatalf("status %d → %v; want NetworkError", tc.status, err)
			}
		}
	}
}

func TestRateLimitErrorRetryAfter(t *testing.T) {
	err := ClassifyHTTPError("test", http.StatusTooManyRequests, 5*time.Second, "slow down", nil)
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want RateLimitError", err)
	}
	if rl.RetryAfter != 5*time.Second {
		t.Fatalf("RetryAfter = %v, want 5s", rl.RetryAfter)
	}
}

func TestSyntheticDryRunStream(t *testing.T) {
	s := SyntheticDryRunStream()
	defer s.Close()
	ev, err := s.Next(context.Background())
	if err != nil {
		t.Fatalf("Next returned %v", err)
	}
	if ev.Kind() != ai.EventStop {
		t.Fatalf("Next kind = %q, want stop", ev.Kind())
	}
}

func TestApplyMiddlewareWraps(t *testing.T) {
	called := 0
	mw := func(next http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			called++
			return next.RoundTrip(r)
		})
	}
	co := &CommonOptions{Middlewares: []Middleware{mw}}
	client := co.ApplyMiddleware(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})})
	req, _ := http.NewRequest("GET", "http://x/", nil)
	if _, err := client.Do(req); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if called != 1 {
		t.Fatalf("middleware not invoked: called=%d", called)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestFakeProviderImplementsNewInterface(t *testing.T) {
	p := NewFakeProvider()
	defer p.Close()

	caps := p.Capabilities("fake-1")
	if !caps.Vision || !caps.Tools || !caps.Streaming {
		t.Fatalf("FakeProvider default caps = %+v, want Vision+Tools+Streaming", caps)
	}

	tokens, err := p.CountTokens(context.Background(), "fake-1", ai.Context{
		Messages: []ai.Message{{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if tokens <= 0 {
		t.Fatalf("CountTokens = %d, want > 0", tokens)
	}

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !p.IsClosed() {
		t.Fatal("FakeProvider IsClosed = false after Close")
	}
	// Close is idempotent.
	if err := p.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestFakeProviderCountTokensOverride(t *testing.T) {
	p := NewFakeProvider(WithFakeCountTokens(func(modelID string, c ai.Context) (int64, error) {
		return 999, nil
	}))
	got, err := p.CountTokens(context.Background(), "x", ai.Context{})
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if got != 999 {
		t.Fatalf("CountTokens = %d, want 999", got)
	}
}
