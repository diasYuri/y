package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// mockOAuthTransport is a reusable HTTP mock for OAuth tests.
type mockOAuthTransport struct {
	responses []*http.Response
	idx       int
}

func (m *mockOAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if m.idx >= len(m.responses) {
		return nil, io.EOF
	}
	r := m.responses[m.idx]
	m.idx++
	return r, nil
}

func oauthMockResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func withOAuthMockClient(t *testing.T, responses []*http.Response) {
	t.Helper()
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &mockOAuthTransport{responses: responses}}
	t.Cleanup(func() { http.DefaultClient = old })
}

func TestBuildAuthURL(t *testing.T) {
	url, err := buildAuthURL(
		"https://auth.example.com/authorize",
		"client-1",
		"http://localhost:1234/callback",
		"challenge-abc",
		"state-xyz",
	)
	if err != nil {
		t.Fatalf("buildAuthURL: %v", err)
	}
	if !strings.Contains(url, "client_id=client-1") {
		t.Fatalf("url missing client_id: %s", url)
	}
	if !strings.Contains(url, "redirect_uri=http%3A%2F%2Flocalhost%3A1234%2Fcallback") {
		t.Fatalf("url missing redirect_uri: %s", url)
	}
	if !strings.Contains(url, "code_challenge=challenge-abc") {
		t.Fatalf("url missing code_challenge: %s", url)
	}
	if !strings.Contains(url, "code_challenge_method=S256") {
		t.Fatalf("url missing code_challenge_method: %s", url)
	}
	if !strings.Contains(url, "state=state-xyz") {
		t.Fatalf("url missing state: %s", url)
	}
	if !strings.Contains(url, "scope=openid+profile") {
		t.Fatalf("url missing scope: %s", url)
	}
}

func TestExchangeTokenSuccess(t *testing.T) {
	withOAuthMockClient(t, []*http.Response{
		oauthMockResponse(200, `{"access_token":"tok-123","refresh_token":"rt-456","expires_in":3600,"token_type":"bearer"}`),
	})

	creds, err := exchangeToken(context.Background(), "http://example.com/token", "client", "http://localhost/cb", "code-abc", "verifier-xyz")
	if err != nil {
		t.Fatalf("exchangeToken: %v", err)
	}
	if creds.ProviderID != "anthropic" {
		t.Fatalf("ProviderID = %q, want anthropic", creds.ProviderID)
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

func TestExchangeTokenErrorResponse(t *testing.T) {
	withOAuthMockClient(t, []*http.Response{
		oauthMockResponse(401, `{"error":"invalid_client"}`),
	})

	_, err := exchangeToken(context.Background(), "http://example.com/token", "client", "http://localhost/cb", "code", "verifier")
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %q, want containing '401'", err.Error())
	}
}

func TestExchangeTokenNoExpiry(t *testing.T) {
	withOAuthMockClient(t, []*http.Response{
		oauthMockResponse(200, `{"access_token":"tok-123"}`),
	})

	creds, err := exchangeToken(context.Background(), "http://example.com/token", "client", "http://localhost/cb", "code", "verifier")
	if err != nil {
		t.Fatalf("exchangeToken: %v", err)
	}
	if !creds.ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt should be zero when no expires_in")
	}
}

func TestAnthropicRefreshSuccess(t *testing.T) {
	withOAuthMockClient(t, []*http.Response{
		oauthMockResponse(200, `{"access_token":"new-tok","refresh_token":"new-rt","expires_in":1800,"token_type":"bearer"}`),
	})

	creds, err := AnthropicRefresh(context.Background(), "old-rt")
	if err != nil {
		t.Fatalf("AnthropicRefresh: %v", err)
	}
	if creds.AccessToken != "new-tok" {
		t.Fatalf("AccessToken = %q, want new-tok", creds.AccessToken)
	}
	if creds.ProviderID != "anthropic" {
		t.Fatalf("ProviderID = %q, want anthropic", creds.ProviderID)
	}
}

func TestAnthropicRefreshError(t *testing.T) {
	withOAuthMockClient(t, []*http.Response{
		oauthMockResponse(400, `{"error":"invalid_grant"}`),
	})

	_, err := AnthropicRefresh(context.Background(), "bad-rt")
	if err == nil {
		t.Fatal("expected error for refresh failure")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error = %q, want containing '400'", err.Error())
	}
}

func TestOpenAIRefreshSuccess(t *testing.T) {
	withOAuthMockClient(t, []*http.Response{
		oauthMockResponse(200, `{"access_token":"openai-tok","expires_in":3600}`),
	})

	creds, err := OpenAIRefresh(context.Background(), "old-rt")
	if err != nil {
		t.Fatalf("OpenAIRefresh: %v", err)
	}
	if creds.AccessToken != "openai-tok" {
		t.Fatalf("AccessToken = %q, want openai-tok", creds.AccessToken)
	}
	if creds.ProviderID != "openai" {
		t.Fatalf("ProviderID = %q, want openai", creds.ProviderID)
	}
}

func TestOpenAIRefreshError(t *testing.T) {
	withOAuthMockClient(t, []*http.Response{
		oauthMockResponse(403, `{"error":"access_denied"}`),
	})

	_, err := OpenAIRefresh(context.Background(), "bad-rt")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error = %q, want containing '403'", err.Error())
	}
}

func TestGoogleRefreshSuccess(t *testing.T) {
	withOAuthMockClient(t, []*http.Response{
		oauthMockResponse(200, `{"access_token":"google-tok","refresh_token":"google-rt","expires_in":7200,"token_type":"Bearer"}`),
	})

	creds, err := GoogleRefresh(context.Background(), "old-rt")
	if err != nil {
		t.Fatalf("GoogleRefresh: %v", err)
	}
	if creds.AccessToken != "google-tok" {
		t.Fatalf("AccessToken = %q, want google-tok", creds.AccessToken)
	}
	if creds.ProviderID != "google" {
		t.Fatalf("ProviderID = %q, want google", creds.ProviderID)
	}
	if creds.TokenType != "Bearer" {
		t.Fatalf("TokenType = %q, want Bearer", creds.TokenType)
	}
}

func TestGoogleRefreshError(t *testing.T) {
	withOAuthMockClient(t, []*http.Response{
		oauthMockResponse(400, `{"error":"invalid_request"}`),
	})

	_, err := GoogleRefresh(context.Background(), "bad-rt")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExchangeGoogleTokenSuccess(t *testing.T) {
	withOAuthMockClient(t, []*http.Response{
		oauthMockResponse(200, `{"access_token":"g-tok","expires_in":3600}`),
	})

	creds, err := exchangeGoogleToken(context.Background(), "client", "http://localhost/cb", "code", "verifier")
	if err != nil {
		t.Fatalf("exchangeGoogleToken: %v", err)
	}
	if creds.AccessToken != "g-tok" {
		t.Fatalf("AccessToken = %q, want g-tok", creds.AccessToken)
	}
	if creds.ProviderID != "google" {
		t.Fatalf("ProviderID = %q, want google", creds.ProviderID)
	}
}

func TestExchangeOpenAITokenSuccess(t *testing.T) {
	withOAuthMockClient(t, []*http.Response{
		oauthMockResponse(200, `{"access_token":"oa-tok","refresh_token":"oa-rt","expires_in":3600}`),
	})

	creds, err := exchangeOpenAIToken(context.Background(), "http://example.com/token", "client", "http://localhost/cb", "code", "verifier")
	if err != nil {
		t.Fatalf("exchangeOpenAIToken: %v", err)
	}
	if creds.AccessToken != "oa-tok" {
		t.Fatalf("AccessToken = %q, want oa-tok", creds.AccessToken)
	}
	if creds.ProviderID != "openai" {
		t.Fatalf("ProviderID = %q, want openai", creds.ProviderID)
	}
}

func TestGitHubCopilotLoginDeviceCodeError(t *testing.T) {
	withOAuthMockClient(t, []*http.Response{
		oauthMockResponse(400, `{"error":"invalid_client"}`),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := GitHubCopilotLogin(ctx)
	if err == nil {
		t.Fatal("expected error for device code failure")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error = %q, want containing '400'", err.Error())
	}
}
