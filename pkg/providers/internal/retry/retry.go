// Package retry provides exponential backoff retry logic for provider HTTP requests.
package retry

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"net/http"
	"time"
)

// Config holds retry parameters.
type Config struct {
	MaxRetries    int
	InitialDelay  time.Duration
	MaxDelay      time.Duration
	BackoffFactor float64
	Jitter        float64 // 0-1 fraction of delay to add as random jitter
}

// DefaultConfig returns a sensible default retry configuration.
func DefaultConfig() Config {
	return Config{
		MaxRetries:    3,
		InitialDelay:  500 * time.Millisecond,
		MaxDelay:      30 * time.Second,
		BackoffFactor: 2.0,
		Jitter:        0.1,
	}
}

// IsRetryableHTTPStatus reports whether an HTTP status code warrants a retry.
func IsRetryableHTTPStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	}
	return false
}

// IsRetryableError reports whether an error warrants a retry.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return true
}

// Do executes fn with exponential backoff retry.
// If ctx is cancelled or the deadline is exceeded, it returns immediately.
func Do(ctx context.Context, cfg Config, fn func() error) error {
	if cfg.MaxRetries <= 0 {
		return fn()
	}
	var lastErr error
	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if attempt == cfg.MaxRetries {
			break
		}
		delay := computeDelay(cfg, attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func computeDelay(cfg Config, attempt int) time.Duration {
	delay := cfg.InitialDelay * time.Duration(math.Pow(cfg.BackoffFactor, float64(attempt)))
	if delay > cfg.MaxDelay {
		delay = cfg.MaxDelay
	}
	if cfg.Jitter > 0 {
		jitter := time.Duration(float64(delay) * cfg.Jitter * (rand.Float64()*2 - 1))
		delay += jitter
	}
	if delay < 0 {
		delay = cfg.InitialDelay
	}
	return delay
}
