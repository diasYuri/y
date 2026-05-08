package auth

import (
	"testing"
	"time"
)

func TestCredentialsIsExpiredNil(t *testing.T) {
	var c *Credentials
	if !c.IsExpired() {
		t.Fatal("IsExpired() = false for nil, want true")
	}
}

func TestCredentialsIsExpiredFuture(t *testing.T) {
	c := &Credentials{ExpiresAt: time.Now().Add(time.Hour)}
	if c.IsExpired() {
		t.Fatal("IsExpired() = true for future expiry, want false")
	}
}

func TestCredentialsIsExpiredPast(t *testing.T) {
	c := &Credentials{ExpiresAt: time.Now().Add(-time.Hour)}
	if !c.IsExpired() {
		t.Fatal("IsExpired() = false for past expiry, want true")
	}
}

func TestCredentialsIsExpiredWithLeeway(t *testing.T) {
	// 90 seconds before expiry should still be valid (leeway is 60s, so expired at ExpiresAt-60s).
	c := &Credentials{ExpiresAt: time.Now().Add(90 * time.Second)}
	if c.IsExpired() {
		t.Fatal("IsExpired() = true for 90s before expiry, want false")
	}

	// 30 seconds before expiry is inside the 60s leeway window, so it IS expired.
	c2 := &Credentials{ExpiresAt: time.Now().Add(30 * time.Second)}
	if !c2.IsExpired() {
		t.Fatal("IsExpired() = false for 30s before expiry, want true (inside 60s leeway)")
	}

	// 70 seconds past expiry should be expired.
	c3 := &Credentials{ExpiresAt: time.Now().Add(-70 * time.Second)}
	if !c3.IsExpired() {
		t.Fatal("IsExpired() = false for 70s past expiry, want true")
	}
}

func TestCredentialsToJSON(t *testing.T) {
	c := &Credentials{
		ProviderID:   "anthropic",
		AccessToken:  "sk-test",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		TokenType:    "bearer",
	}
	data, err := c.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("ToJSON returned empty")
	}
}

func TestCredentialsFromJSON(t *testing.T) {
	input := `{"provider_id":"openai","access_token":"sk-abc","refresh_token":"rt","expires_at":"2026-01-15T10:30:00Z","token_type":"bearer"}`
	c, err := CredentialsFromJSON([]byte(input))
	if err != nil {
		t.Fatalf("CredentialsFromJSON: %v", err)
	}
	if c.ProviderID != "openai" {
		t.Fatalf("ProviderID = %q, want openai", c.ProviderID)
	}
	if c.AccessToken != "sk-abc" {
		t.Fatalf("AccessToken = %q, want sk-abc", c.AccessToken)
	}
	if c.RefreshToken != "rt" {
		t.Fatalf("RefreshToken = %q, want rt", c.RefreshToken)
	}
	if c.TokenType != "bearer" {
		t.Fatalf("TokenType = %q, want bearer", c.TokenType)
	}
}

func TestCredentialsFromJSONInvalid(t *testing.T) {
	_, err := CredentialsFromJSON([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestCredentialsRoundTrip(t *testing.T) {
	original := &Credentials{
		ProviderID:   "google",
		AccessToken:  "token-123",
		RefreshToken: "refresh-456",
		ExpiresAt:    time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		TokenType:    "Bearer",
	}
	data, err := original.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	decoded, err := CredentialsFromJSON(data)
	if err != nil {
		t.Fatalf("CredentialsFromJSON: %v", err)
	}
	if decoded.ProviderID != original.ProviderID {
		t.Fatalf("ProviderID = %q, want %q", decoded.ProviderID, original.ProviderID)
	}
	if decoded.AccessToken != original.AccessToken {
		t.Fatalf("AccessToken = %q, want %q", decoded.AccessToken, original.AccessToken)
	}
	if decoded.RefreshToken != original.RefreshToken {
		t.Fatalf("RefreshToken = %q, want %q", decoded.RefreshToken, original.RefreshToken)
	}
	if decoded.TokenType != original.TokenType {
		t.Fatalf("TokenType = %q, want %q", decoded.TokenType, original.TokenType)
	}
	if !decoded.ExpiresAt.Equal(original.ExpiresAt) {
		t.Fatalf("ExpiresAt = %v, want %v", decoded.ExpiresAt, original.ExpiresAt)
	}
}
