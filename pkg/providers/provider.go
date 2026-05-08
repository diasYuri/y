package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/yuri/y/pkg/ai"
)

// ErrStreamClosed is returned by EventStream.Next after Close has been called.
var ErrStreamClosed = errors.New("provider event stream closed")

// Provider streams normalized AI events for one provider family.
//
// Lifecycle:
//   - Concrete providers are constructed via package-level New(...) functions.
//   - Stream() may be called concurrently; each call returns its own EventStream.
//   - Close() releases shared resources (HTTP transports, OAuth refreshers,
//     background goroutines). After Close() the provider must not be used.
//
// Capability semantics:
//   - CountTokens returns an authoritative count when the provider exposes a
//     token-count endpoint, and a best-effort estimate otherwise.
//   - Capabilities() returns the set of features the given model is known to
//     support. Unknown model IDs return a zero-value Capabilities (all false).
type Provider interface {
	ID() string
	Models(ctx context.Context) ([]ai.Model, error)
	Stream(ctx context.Context, req StreamRequest) (EventStream, error)
	// CountTokens returns the number of input tokens the given Context would
	// consume. Implementations must return a positive number on success.
	CountTokens(ctx context.Context, modelID string, c ai.Context) (int64, error)
	// Capabilities returns the feature flags supported by the named model.
	// An unknown modelID returns a zero-value Capabilities.
	Capabilities(modelID string) Capabilities
	// Close releases provider-level resources (idle HTTP connections, refresh
	// goroutines, etc.). Calling Close on a nil receiver is a no-op. Close is
	// idempotent.
	Close() error
}

// EventStream is a pull-based, cancelable stream of normalized provider events.
//
// Close semantics: Close cancels any in-flight HTTP request (the underlying
// context is cancelled), releases the response body, and unblocks any pending
// Next() call with ErrStreamClosed. Close MUST NOT block waiting for the
// upstream goroutine to finish draining; it returns immediately. Subsequent
// Next() calls after Close return ErrStreamClosed. Close is idempotent.
//
// Implementations return io.EOF after the final event has been consumed.
type EventStream interface {
	Next(ctx context.Context) (ai.Event, error)
	Close() error
}

// StreamRequest is the provider-neutral request shape consumed by Provider.
type StreamRequest struct {
	Model   ai.Model
	Context ai.Context
	Options StreamOptions
}

// StreamOptions contains cross-provider options.
//
// API key precedence (uniform across providers):
//  1. StreamOptions.APIKey (per-request override) — wins if non-empty.
//  2. WithAPIKey constructor option.
//  3. Provider-specific environment variables via the configured env lookup
//     (see WithEnvLookup on each provider).
//
// Provider-specific knobs may be passed via Extras (typed) or the deprecated
// Metadata field.
type StreamOptions struct {
	Temperature     *float64
	MaxTokens       int64
	APIKey          string
	Transport       ai.Transport
	CacheRetention  ai.CacheRetention
	SessionID       string
	Headers         map[string]string
	Timeout         time.Duration
	MaxRetries      int
	MaxRetryDelay   time.Duration
	Reasoning       ai.ThinkingLevel
	ThinkingBudgets map[ai.ThinkingLevel]int64
	// Extras holds typed provider-specific extension keys. Prefer this over
	// Metadata. Use WithProviderExtra to construct.
	Extras ProviderExtras
	// Deprecated: Metadata is retained for backwards compatibility with callers
	// that pass a raw JSON blob. New code should use Extras / WithProviderExtra.
	Metadata json.RawMessage
}

// ProviderExtras is a typed bag of provider-specific extension values. It is
// intentionally a map so that callers can compose multiple WithProviderExtra
// helpers without colliding.
type ProviderExtras map[string]any

// WithProviderExtra returns a copy of ProviderExtras with the given key set.
// It is safe to call on a nil receiver and returns a fresh map.
func (e ProviderExtras) WithProviderExtra(key string, value any) ProviderExtras {
	out := make(ProviderExtras, len(e)+1)
	for k, v := range e {
		out[k] = v
	}
	out[key] = value
	return out
}

// WithProviderExtra is a convenience that returns a ProviderExtras seeded with
// the given key/value. It is the canonical way to add a single provider extra.
func WithProviderExtra(key string, value any) ProviderExtras {
	return ProviderExtras{key: value}
}

// Capabilities describes the feature set a model is known to support.
// Unknown / unset fields default to false. New fields may be appended without
// breaking existing callers.
type Capabilities struct {
	// Vision is true when the model accepts image inputs.
	Vision bool
	// Tools is true when the model accepts tool/function declarations.
	Tools bool
	// Reasoning is true when the model honours ai.ThinkingLevel and
	// StreamOptions.ThinkingBudgets. Providers that do not support reasoning
	// silently ignore those options.
	Reasoning bool
	// PromptCache is true when the model honours StreamOptions.CacheRetention.
	PromptCache bool
	// JSONMode is true when the model supports forced-JSON output.
	JSONMode bool
	// Streaming is true when the model supports SSE streaming. All currently
	// supported providers stream.
	Streaming bool
}

// RetryPolicy controls request-level retries for transient failures (network
// errors and 5xx / 429 statuses). MaxRetries == 0 disables retries.
type RetryPolicy struct {
	MaxRetries    int
	InitialDelay  time.Duration
	MaxDelay      time.Duration
	BackoffFactor float64
	Jitter        float64
}

// DefaultRetryPolicy returns a sensible default policy.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:    3,
		InitialDelay:  500 * time.Millisecond,
		MaxDelay:      30 * time.Second,
		BackoffFactor: 2.0,
		Jitter:        0.1,
	}
}

// Middleware wraps an http.RoundTripper. Providers compose middlewares onto
// their transport in registration order (first registered → outermost).
type Middleware func(http.RoundTripper) http.RoundTripper

// RequestInspector receives the fully-built http.Request immediately before it
// would be sent. It is invoked synchronously and must not retain the request
// past the call. When DryRun mode is active the request is not sent and the
// stream emits a synthetic stop.
type RequestInspector func(req *http.Request)

// Typed error types. Providers map provider-specific HTTP/JSON errors into
// these so that callers can use errors.As to react to common conditions.

// RateLimitError indicates the provider rate limited the request. RetryAfter,
// if positive, is the suggested wait derived from the Retry-After header.
type RateLimitError struct {
	Provider   string
	StatusCode int
	RetryAfter time.Duration
	Message    string
	Err        error
}

func (e *RateLimitError) Error() string {
	if e == nil {
		return "rate limited"
	}
	if e.Message != "" {
		return fmt.Sprintf("%s: rate limited: %s", e.Provider, e.Message)
	}
	return fmt.Sprintf("%s: rate limited (status %d)", e.Provider, e.StatusCode)
}

func (e *RateLimitError) Unwrap() error { return e.Err }

// AuthError indicates an authentication or authorization failure (401/403).
type AuthError struct {
	Provider   string
	StatusCode int
	Message    string
	Err        error
}

func (e *AuthError) Error() string {
	if e == nil {
		return "auth error"
	}
	if e.Message != "" {
		return fmt.Sprintf("%s: auth error: %s", e.Provider, e.Message)
	}
	return fmt.Sprintf("%s: auth error (status %d)", e.Provider, e.StatusCode)
}

func (e *AuthError) Unwrap() error { return e.Err }

// ContextOverflowError indicates the request exceeded the model's context
// window or output limit.
type ContextOverflowError struct {
	Provider   string
	StatusCode int
	Message    string
	Err        error
}

func (e *ContextOverflowError) Error() string {
	if e == nil {
		return "context overflow"
	}
	if e.Message != "" {
		return fmt.Sprintf("%s: context overflow: %s", e.Provider, e.Message)
	}
	return fmt.Sprintf("%s: context overflow", e.Provider)
}

func (e *ContextOverflowError) Unwrap() error { return e.Err }

// NetworkError wraps a transport-level failure (DNS, TCP, TLS, timeout) or a
// 5xx response that could not be retried.
type NetworkError struct {
	Provider   string
	StatusCode int
	Message    string
	Err        error
}

func (e *NetworkError) Error() string {
	if e == nil {
		return "network error"
	}
	if e.Message != "" {
		return fmt.Sprintf("%s: network error: %s", e.Provider, e.Message)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: network error: %s", e.Provider, e.Err.Error())
	}
	return fmt.Sprintf("%s: network error (status %d)", e.Provider, e.StatusCode)
}

func (e *NetworkError) Unwrap() error { return e.Err }

// ClassifyHTTPError maps an HTTP status code and response body into the
// matching typed error. providerID is the textual identifier used in error
// messages. retryAfter may be zero if no Retry-After header was present.
func ClassifyHTTPError(providerID string, statusCode int, retryAfter time.Duration, body string, cause error) error {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return &RateLimitError{Provider: providerID, StatusCode: statusCode, RetryAfter: retryAfter, Message: body, Err: cause}
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return &AuthError{Provider: providerID, StatusCode: statusCode, Message: body, Err: cause}
	case statusCode == http.StatusRequestEntityTooLarge || statusCode == 413:
		return &ContextOverflowError{Provider: providerID, StatusCode: statusCode, Message: body, Err: cause}
	case statusCode >= 500 && statusCode < 600:
		return &NetworkError{Provider: providerID, StatusCode: statusCode, Message: body, Err: cause}
	}
	return &NetworkError{Provider: providerID, StatusCode: statusCode, Message: body, Err: cause}
}
