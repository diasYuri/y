package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/tools"
)

// TestIsTransientRateLimit verifies that a *providers.RateLimitError is always
// treated as transient and that WithMaxRetries actually retries the request.
func TestIsTransientRateLimit(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Err: &providers.RateLimitError{Provider: "fake", StatusCode: 429},
		},
		providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: "ok"},
				ai.StopEvent{Reason: ai.StopReasonStop},
			},
		},
	))
	a := New(provider, tools.NewRegistry(), WithMaxRetries(2))
	res, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res.State != StateCompleted {
		t.Fatalf("state = %q, want completed", res.State)
	}
	if got := provider.CallCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 (1 retry)", got)
	}
}

// TestIsTransientNetworkError5xx verifies *providers.NetworkError with a 5xx
// status code triggers a retry.
func TestIsTransientNetworkError5xx(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Err: &providers.NetworkError{Provider: "fake", StatusCode: 503},
		},
		providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: "ok"},
				ai.StopEvent{Reason: ai.StopReasonStop},
			},
		},
	))
	a := New(provider, tools.NewRegistry(), WithMaxRetries(2))
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got := provider.CallCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 (1 retry)", got)
	}
}

// TestIsTransientNetworkErrorTransport confirms a NetworkError with no status
// code (transport-level failure) is transient.
func TestIsTransientNetworkErrorTransport(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Err: &providers.NetworkError{Provider: "fake", StatusCode: 0, Err: errors.New("dial tcp: connection refused")},
		},
		providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: "ok"},
				ai.StopEvent{Reason: ai.StopReasonStop},
			},
		},
	))
	a := New(provider, tools.NewRegistry(), WithMaxRetries(2))
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got := provider.CallCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 (1 retry)", got)
	}
}

// TestIsTransientNetworkError4xxNotRetried verifies that a NetworkError with a
// 4xx status is not retried (even when MaxRetries > 0).
func TestIsTransientNetworkError4xxNotRetried(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Err: &providers.NetworkError{Provider: "fake", StatusCode: 400},
		},
	))
	a := New(provider, tools.NewRegistry(), WithMaxRetries(3))
	if _, err := a.Run(context.Background(), "hi"); err == nil {
		t.Fatal("expected non-retryable network error")
	}
	if got := provider.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 (no retries)", got)
	}
}

// TestIsTransientAuthErrorNotRetried verifies AuthError is not retried.
func TestIsTransientAuthErrorNotRetried(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Err: &providers.AuthError{Provider: "fake", StatusCode: 401},
		},
	))
	a := New(provider, tools.NewRegistry(), WithMaxRetries(3))
	if _, err := a.Run(context.Background(), "hi"); err == nil {
		t.Fatal("expected auth error")
	}
	if got := provider.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 (auth not transient)", got)
	}
}

// TestIsTransientContextOverflowNotRetried verifies ContextOverflowError is
// not retried.
func TestIsTransientContextOverflowNotRetried(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Err: &providers.ContextOverflowError{Provider: "fake", StatusCode: 413},
		},
	))
	a := New(provider, tools.NewRegistry(), WithMaxRetries(3))
	if _, err := a.Run(context.Background(), "hi"); err == nil {
		t.Fatal("expected context overflow error")
	}
	if got := provider.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 (overflow not transient)", got)
	}
}

// TestRateLimitRetryAfterRespected verifies the configured backoff is
// stretched up to RateLimitError.RetryAfter so we never re-issue too soon.
func TestRateLimitRetryAfterRespected(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(
		providers.FakeResponse{
			Err: &providers.RateLimitError{Provider: "fake", StatusCode: 429, RetryAfter: 200 * time.Millisecond},
		},
		providers.FakeResponse{
			Events: []ai.Event{
				ai.TextDelta{Text: "ok"},
				ai.StopEvent{Reason: ai.StopReasonStop},
			},
		},
	))
	// MaxRetryDelay below 200ms forces the backoff calculation to be smaller
	// than the Retry-After hint; the agent must still wait the full hint.
	a := New(provider, tools.NewRegistry(),
		WithMaxRetries(1),
		WithMaxRetryDelay(50*time.Millisecond),
	)
	start := time.Now()
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 200*time.Millisecond {
		t.Fatalf("retry took %v, want at least 200ms (Retry-After honored)", elapsed)
	}
	if got := provider.CallCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
}

// TestUsageObserverFiresOnHookedResponse confirms WithUsageObserver is invoked
// when a BeforeRequest hook short-circuits the call.
func TestUsageObserverFiresOnHookedResponse(t *testing.T) {
	provider := providers.NewFakeProvider(providers.WithFakeResponses(providers.FakeResponse{
		Events: []ai.Event{
			ai.TextDelta{Text: "should not be used"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}))

	hook := BeforeRequestHook(func(ctx context.Context, req *providers.StreamRequest) (*HookedResponse, error) {
		return &HookedResponse{
			Message: ai.Message{
				Role:    ai.RoleAssistant,
				Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "cached"}},
			},
			Usage:      ai.Usage{InputTokens: 9, OutputTokens: 13, TotalTokens: 22},
			StopReason: ai.StopReasonStop,
		}, nil
	})

	var mu sync.Mutex
	var observed []ai.Usage
	a := New(provider, tools.NewRegistry(),
		WithBeforeRequest(hook),
		WithUsageObserver(func(_ UsageOrigin, u ai.Usage) {
			mu.Lock()
			observed = append(observed, u)
			mu.Unlock()
		}),
	)
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observed) != 1 {
		t.Fatalf("observer fired %d times, want 1", len(observed))
	}
	if observed[0].InputTokens != 9 || observed[0].OutputTokens != 13 {
		t.Fatalf("observed usage = %+v, want input=9 output=13", observed[0])
	}
}
