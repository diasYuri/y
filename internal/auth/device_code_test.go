package auth

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type mockRoundTripper struct {
	responses []*http.Response
	idx       int
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if m.idx >= len(m.responses) {
		return nil, io.EOF
	}
	r := m.responses[m.idx]
	m.idx++
	return r, nil
}

func mockResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func mockResponseWithHeader(status int, body string, header http.Header) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}
}

func withMockClient(t *testing.T, responses []*http.Response) {
	t.Helper()
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &mockRoundTripper{responses: responses}}
	t.Cleanup(func() { http.DefaultClient = old })
}

func TestPollDeviceTokenSuccess(t *testing.T) {
	withMockClient(t, []*http.Response{
		mockResponse(200, `{"access_token":"tok-123","refresh_token":"rt-456","expires_in":3600,"token_type":"bearer"}`),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	creds, err := PollDeviceToken(ctx, "http://example.com/token", "client-1", "device-1", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.AccessToken != "tok-123" {
		t.Fatalf("AccessToken = %q, want tok-123", creds.AccessToken)
	}
	if creds.RefreshToken != "rt-456" {
		t.Fatalf("RefreshToken = %q, want rt-456", creds.RefreshToken)
	}
	if creds.TokenType != "bearer" {
		t.Fatalf("TokenType = %q, want bearer", creds.TokenType)
	}
	if creds.ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt is zero")
	}
}

func TestPollDeviceTokenAuthorizationPending(t *testing.T) {
	withMockClient(t, []*http.Response{
		mockResponse(400, `{"error":"authorization_pending"}`),
		mockResponse(400, `{"error":"authorization_pending"}`),
		mockResponse(200, `{"access_token":"tok-ok"}`),
	})

	// Minimum tick interval is 5s; 3 requests need ~10s.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	creds, err := PollDeviceToken(ctx, "http://example.com/token", "client", "device", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.AccessToken != "tok-ok" {
		t.Fatalf("AccessToken = %q, want tok-ok", creds.AccessToken)
	}
}

func TestPollDeviceTokenSlowDown(t *testing.T) {
	withMockClient(t, []*http.Response{
		mockResponse(400, `{"error":"slow_down"}`),
		mockResponse(200, `{"access_token":"tok-ok"}`),
	})

	// slow_down increases interval by 5s; need at least 10s.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	creds, err := PollDeviceToken(ctx, "http://example.com/token", "client", "device", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.AccessToken != "tok-ok" {
		t.Fatalf("AccessToken = %q, want tok-ok", creds.AccessToken)
	}
}

func TestPollDeviceTokenExpired(t *testing.T) {
	withMockClient(t, []*http.Response{
		mockResponse(400, `{"error":"expired_token"}`),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := PollDeviceToken(ctx, "http://example.com/token", "client", "device", 1)
	if err == nil {
		t.Fatal("expected error for expired_token")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("error = %q, want containing 'expired'", err.Error())
	}
}

func TestPollDeviceTokenAccessDenied(t *testing.T) {
	withMockClient(t, []*http.Response{
		mockResponse(400, `{"error":"access_denied"}`),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := PollDeviceToken(ctx, "http://example.com/token", "client", "device", 1)
	if err == nil {
		t.Fatal("expected error for access_denied")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Fatalf("error = %q, want containing 'denied'", err.Error())
	}
}

func TestPollDeviceTokenContextCancel(t *testing.T) {
	withMockClient(t, []*http.Response{
		// Response will never be consumed because context is cancelled before first tick.
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := PollDeviceToken(ctx, "http://example.com/token", "client", "device", 1)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if err != context.Canceled {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestPollDeviceTokenRetryAfter(t *testing.T) {
	withMockClient(t, []*http.Response{
		mockResponseWithHeader(429, "", http.Header{"Retry-After": []string{"1"}}),
		mockResponse(200, `{"access_token":"tok-ok"}`),
	})

	// Retry-After sleep + ticker interval; need at least 6s.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	creds, err := PollDeviceToken(ctx, "http://example.com/token", "client", "device", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.AccessToken != "tok-ok" {
		t.Fatalf("AccessToken = %q, want tok-ok", creds.AccessToken)
	}
}

func TestPollDeviceTokenUnknownError(t *testing.T) {
	withMockClient(t, []*http.Response{
		mockResponse(400, `{"error":"invalid_request"}`),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := PollDeviceToken(ctx, "http://example.com/token", "client", "device", 1)
	if err == nil {
		t.Fatal("expected error for unknown error code")
	}
	if !strings.Contains(err.Error(), "invalid_request") {
		t.Fatalf("error = %q, want containing 'invalid_request'", err.Error())
	}
}

func TestPollDeviceTokenNonJSONError(t *testing.T) {
	withMockClient(t, []*http.Response{
		mockResponse(500, "internal server error"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := PollDeviceToken(ctx, "http://example.com/token", "client", "device", 1)
	if err == nil {
		t.Fatal("expected error for non-JSON error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error = %q, want containing '500'", err.Error())
	}
}

func TestPollDeviceTokenEmptyBodySuccess(t *testing.T) {
	withMockClient(t, []*http.Response{
		mockResponse(200, `{"access_token":"minimal"}`),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	creds, err := PollDeviceToken(ctx, "http://example.com/token", "client", "device", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.AccessToken != "minimal" {
		t.Fatalf("AccessToken = %q, want minimal", creds.AccessToken)
	}
	if !creds.ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt should be zero when no expires_in")
	}
}

func TestPollDeviceTokenRequestBody(t *testing.T) {
	var capturedBody []byte
	rt := &mockRoundTripper{
		responses: []*http.Response{
			mockResponse(200, `{"access_token":"ok"}`),
		},
	}
	// Wrap to capture body.
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &bodyCapturingRoundTripper{inner: rt, body: &capturedBody}}
	defer func() { http.DefaultClient = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := PollDeviceToken(ctx, "http://example.com/token", "my-client", "my-device", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := string(capturedBody)
	if !strings.Contains(body, "grant_type") {
		t.Fatalf("body missing grant_type: %s", body)
	}
	if !strings.Contains(body, "my-client") {
		t.Fatalf("body missing client_id: %s", body)
	}
	if !strings.Contains(body, "my-device") {
		t.Fatalf("body missing device_code: %s", body)
	}
}

type bodyCapturingRoundTripper struct {
	inner *mockRoundTripper
	body  *[]byte
}

func (b *bodyCapturingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		*b.body = data
		req.Body = io.NopCloser(bytes.NewReader(data))
	}
	return b.inner.RoundTrip(req)
}
