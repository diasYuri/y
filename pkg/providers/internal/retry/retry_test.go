package retry

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxRetries != 3 {
		t.Fatalf("MaxRetries = %d, want 3", cfg.MaxRetries)
	}
	if cfg.InitialDelay != 500*time.Millisecond {
		t.Fatalf("InitialDelay = %v, want 500ms", cfg.InitialDelay)
	}
	if cfg.MaxDelay != 30*time.Second {
		t.Fatalf("MaxDelay = %v, want 30s", cfg.MaxDelay)
	}
	if cfg.BackoffFactor != 2.0 {
		t.Fatalf("BackoffFactor = %f, want 2.0", cfg.BackoffFactor)
	}
	if cfg.Jitter != 0.1 {
		t.Fatalf("Jitter = %f, want 0.1", cfg.Jitter)
	}
}

func TestIsRetryableHTTPStatus(t *testing.T) {
	retryable := []int{
		http.StatusTooManyRequests,     // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout,      // 504
	}
	for _, code := range retryable {
		if !IsRetryableHTTPStatus(code) {
			t.Fatalf("IsRetryableHTTPStatus(%d) = false, want true", code)
		}
	}

	nonRetryable := []int{
		http.StatusOK,                  // 200
		http.StatusBadRequest,          // 400
		http.StatusUnauthorized,        // 401
		http.StatusForbidden,           // 403
		http.StatusNotFound,            // 404
		http.StatusMethodNotAllowed,    // 405
		http.StatusConflict,            // 409
		http.StatusUnprocessableEntity, // 422
	}
	for _, code := range nonRetryable {
		if IsRetryableHTTPStatus(code) {
			t.Fatalf("IsRetryableHTTPStatus(%d) = true, want false", code)
		}
	}
}

func TestIsRetryableErrorNil(t *testing.T) {
	if IsRetryableError(nil) {
		t.Fatal("IsRetryableError(nil) = true, want false")
	}
}

func TestIsRetryableErrorTimeout(t *testing.T) {
	err := &net.DNSError{Err: "timeout", IsTimeout: true}
	if !IsRetryableError(err) {
		t.Fatal("IsRetryableError(timeout) = false, want true")
	}
}

func TestIsRetryableErrorGeneric(t *testing.T) {
	err := errors.New("something failed")
	if !IsRetryableError(err) {
		t.Fatal("IsRetryableError(generic) = false, want true")
	}
}

func TestDoSuccessFirstAttempt(t *testing.T) {
	called := 0
	fn := func() error {
		called++
		return nil
	}
	cfg := Config{MaxRetries: 3, InitialDelay: time.Millisecond}
	err := Do(context.Background(), cfg, fn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called != 1 {
		t.Fatalf("called = %d, want 1", called)
	}
}

func TestDoSuccessAfterRetries(t *testing.T) {
	called := 0
	fn := func() error {
		called++
		if called < 3 {
			return errors.New("transient")
		}
		return nil
	}
	cfg := Config{MaxRetries: 5, InitialDelay: time.Millisecond}
	err := Do(context.Background(), cfg, fn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called != 3 {
		t.Fatalf("called = %d, want 3", called)
	}
}

func TestDoExhaustsRetries(t *testing.T) {
	called := 0
	expectedErr := errors.New("persistent")
	fn := func() error {
		called++
		return expectedErr
	}
	cfg := Config{MaxRetries: 2, InitialDelay: time.Millisecond}
	err := Do(context.Background(), cfg, fn)
	if err != expectedErr {
		t.Fatalf("error = %v, want %v", err, expectedErr)
	}
	if called != 3 { // initial + 2 retries
		t.Fatalf("called = %d, want 3", called)
	}
}

func TestDoZeroMaxRetries(t *testing.T) {
	called := 0
	fn := func() error {
		called++
		return errors.New("fail")
	}
	cfg := Config{MaxRetries: 0}
	err := Do(context.Background(), cfg, fn)
	if err == nil {
		t.Fatal("expected error")
	}
	if called != 1 {
		t.Fatalf("called = %d, want 1", called)
	}
}

func TestDoContextCancelBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fn := func() error { return errors.New("should not be called") }
	cfg := Config{MaxRetries: 3, InitialDelay: time.Millisecond}
	err := Do(ctx, cfg, fn)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestDoContextCancelDuringRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	called := 0
	fn := func() error {
		called++
		if called == 1 {
			cancel() // cancel during first retry wait
		}
		return errors.New("fail")
	}
	cfg := Config{MaxRetries: 5, InitialDelay: 100 * time.Millisecond}
	err := Do(ctx, cfg, fn)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestComputeDelayExponential(t *testing.T) {
	cfg := Config{
		InitialDelay:  100 * time.Millisecond,
		MaxDelay:      10 * time.Second,
		BackoffFactor: 2.0,
		Jitter:        0,
	}
	delays := []time.Duration{
		computeDelay(cfg, 0), // 100ms
		computeDelay(cfg, 1), // 200ms
		computeDelay(cfg, 2), // 400ms
		computeDelay(cfg, 3), // 800ms
	}
	expected := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond}
	for i, want := range expected {
		if delays[i] != want {
			t.Fatalf("delay[%d] = %v, want %v", i, delays[i], want)
		}
	}
}

func TestComputeDelayMaxCap(t *testing.T) {
	cfg := Config{
		InitialDelay:  1 * time.Second,
		MaxDelay:      5 * time.Second,
		BackoffFactor: 10.0,
		Jitter:        0,
	}
	delay := computeDelay(cfg, 2) // would be 100s without cap
	if delay != 5*time.Second {
		t.Fatalf("delay = %v, want 5s", delay)
	}
}

func TestComputeDelayWithJitter(t *testing.T) {
	cfg := Config{
		InitialDelay:  100 * time.Millisecond,
		MaxDelay:      10 * time.Second,
		BackoffFactor: 2.0,
		Jitter:        0.5,
	}
	// With jitter, delay should be within +/- 50% of base
	base := 100 * time.Millisecond
	for i := 0; i < 20; i++ {
		delay := computeDelay(cfg, 0)
		min := time.Duration(float64(base) * 0.5)
		max := time.Duration(float64(base) * 1.5)
		if delay < min || delay > max {
			t.Fatalf("delay %v outside expected range [%v, %v]", delay, min, max)
		}
	}
}

func TestComputeDelayNeverNegative(t *testing.T) {
	cfg := Config{
		InitialDelay:  100 * time.Millisecond,
		MaxDelay:      10 * time.Second,
		BackoffFactor: 2.0,
		Jitter:        10.0, // extreme jitter can make delay negative
	}
	// Run many times to hit the negative jitter path; verify delay is always non-negative.
	foundNegative := false
	for i := 0; i < 200; i++ {
		delay := computeDelay(cfg, 0)
		if delay < 0 {
			t.Fatalf("delay = %v, must never be negative", delay)
		}
		if delay == cfg.InitialDelay {
			foundNegative = true // jitter was negative and triggered fallback
		}
	}
	if !foundNegative {
		t.Skip("jitter never went negative in 200 iterations, but all delays were valid")
	}
}
