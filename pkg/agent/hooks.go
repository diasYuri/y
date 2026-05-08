package agent

import (
	"context"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

// BeforeRequestHook is called before each provider Stream call.
//
// Hooks may inspect or rewrite the outgoing request via the *providers.StreamRequest
// pointer. To short-circuit the call (for example, to serve a cached response),
// return a non-nil response; the agent will append it to the transcript as if it
// had come from the provider and skip the actual Stream call.
//
// Returning an error fails the run unless an [ErrorHook] reclassifies it.
type BeforeRequestHook func(ctx context.Context, req *providers.StreamRequest) (*HookedResponse, error)

// AfterRequestHook is called after every provider response, regardless of
// whether the call was short-circuited by a [BeforeRequestHook]. It is also
// invoked when the request returned an error: in that case `msg` is zero and
// `err` is non-nil. Returning a non-nil error replaces the original error.
type AfterRequestHook func(ctx context.Context, req providers.StreamRequest, msg ai.Message, usage ai.Usage, err error) error

// ErrorHook is called when the run encounters an error from a provider, a tool,
// or the loop itself. The hook may:
//
//   - return nil to swallow the error and let the loop continue from the next
//     turn (only meaningful for transient failures);
//   - return [ErrRetry] to request a single retry of the failing step;
//   - return any other error to replace the original error.
//
// The original error is provided so hooks can classify it (transient vs
// permanent) using errors.Is / errors.As.
type ErrorHook func(ctx context.Context, phase ErrorPhase, err error) error

// ErrorPhase identifies where in the loop an error occurred.
type ErrorPhase string

const (
	ErrorPhaseModelSelect ErrorPhase = "model_select"
	ErrorPhaseRequest     ErrorPhase = "request"
	ErrorPhaseTool        ErrorPhase = "tool"
	ErrorPhaseUnknown     ErrorPhase = "unknown"
)

// HookedResponse is returned by a [BeforeRequestHook] to short-circuit a
// provider call. The Message is appended to the transcript exactly as the
// provider response would have been; Usage is added to the running totals.
// StopReason defaults to [ai.StopReasonStop] when zero.
type HookedResponse struct {
	Message    ai.Message
	Usage      ai.Usage
	StopReason ai.StopReason
}

// Logger is a minimal structured-logger interface used by the agent for
// non-fatal diagnostics (token-estimate fallback, retry decisions, etc.).
// Pass [DiscardLogger] to silence output.
type Logger interface {
	Logf(format string, args ...interface{})
}

// LoggerFunc adapts a plain function to [Logger].
type LoggerFunc func(format string, args ...interface{})

// Logf implements [Logger].
func (f LoggerFunc) Logf(format string, args ...interface{}) { f(format, args...) }

// DiscardLogger is a [Logger] that drops every message.
var DiscardLogger Logger = LoggerFunc(func(string, ...interface{}) {})

// UsageOrigin describes where a [ai.Usage] sample came from.
type UsageOrigin string

const (
	// UsageReported means the provider reported the usage values directly.
	UsageReported UsageOrigin = "reported"
	// UsageEstimated means the agent fell back to its char-based heuristic.
	UsageEstimated UsageOrigin = "estimated"
)

// UsageObserver is invoked once per assistant turn with the usage value and
// its origin. Callers typically use it to feed an external metrics pipeline
// or to detect drift between reported and estimated counts.
type UsageObserver func(origin UsageOrigin, usage ai.Usage)
