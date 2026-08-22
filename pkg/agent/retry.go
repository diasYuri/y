package agent

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

// ErrRetry is a sentinel returned by [ErrorHook] to request that the failing
// step be retried once before propagating an error. The agent honours up to
// [WithMaxRetries] retries per step, after which the original error is
// returned.
var ErrRetry = errors.New("agent: retry requested")

// requestAssistantWithRetry wraps requestAssistant with exponential-backoff
// retry honouring [WithMaxRetries] and the [ErrorHook] decision.
func (a *Agent) requestAssistantWithRetry(ctx context.Context, model ai.Model, turn int) (ai.Message, ai.Usage, ai.StopReason, error) {
	a.mu.Lock()
	maxRetries := a.maxRetries
	maxRetryDelay := a.maxRetryDelay
	logger := a.logger
	a.mu.Unlock()
	if maxRetryDelay <= 0 {
		maxRetryDelay = 5 * time.Second
	}

	delay := 100 * time.Millisecond
	for attempt := 0; ; attempt++ {
		msg, usage, stop, err := a.requestAssistant(ctx, model, turn)
		if err == nil {
			return msg, usage, stop, nil
		}

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return msg, usage, stop, err
		}

		retry := errors.Is(err, ErrRetry) || (attempt < maxRetries && isTransient(err))
		if !retry || attempt >= maxRetries {
			return msg, usage, stop, err
		}

		if logger != nil {
			logger.Logf("agent: retrying provider request after error (attempt=%d, err=%v)", attempt+1, err)
		}

		wait := delay
		var rateLimit *providers.RateLimitError
		if errors.As(err, &rateLimit) && rateLimit.RetryAfter > wait {
			wait = rateLimit.RetryAfter
			if logger != nil {
				logger.Logf("agent: honouring Retry-After hint of %s before next attempt", rateLimit.RetryAfter)
			}
		}

		select {
		case <-ctx.Done():
			return msg, usage, stop, ctx.Err()
		case <-time.After(wait):
		}
		delay *= 2
		if delay > maxRetryDelay {
			delay = maxRetryDelay
		}
	}
}

// isTransient returns true when the error is heuristically retryable
// (network errors, EOF, generic "temporary" errors). Hooks can extend this
// classification by returning [ErrRetry] from [ErrorHook].
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	var rateLimit *providers.RateLimitError
	if errors.As(err, &rateLimit) {
		return true
	}
	var networkErr *providers.NetworkError
	if errors.As(err, &networkErr) {
		return networkErr.StatusCode == 0 || networkErr.StatusCode >= 500
	}
	var authErr *providers.AuthError
	if errors.As(err, &authErr) {
		return false
	}
	var overflowErr *providers.ContextOverflowError
	if errors.As(err, &overflowErr) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	type temporary interface{ Temporary() bool }
	var temporaryErr temporary
	return errors.As(err, &temporaryErr) && temporaryErr.Temporary()
}

// isRecoverable returns true when [Recover] (or [Continue]) can sensibly
// retry the failed step. Currently aligned with [isTransient] plus explicit
// retry signals.
func isRecoverable(err error) bool {
	return err != nil && (errors.Is(err, ErrRetry) || isTransient(err))
}
