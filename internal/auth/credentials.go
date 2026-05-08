package auth

import (
	"encoding/json"
	"time"
)

// Credentials stores OAuth tokens for a single provider.
type Credentials struct {
	ProviderID   string    `json:"provider_id"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	TokenType    string    `json:"token_type,omitempty"`
}

// IsExpired reports whether the token has expired (with 60s leeway).
func (c *Credentials) IsExpired() bool {
	if c == nil {
		return true
	}
	return time.Now().After(c.ExpiresAt.Add(-60 * time.Second))
}

// ToJSON serializes credentials to JSON.
func (c *Credentials) ToJSON() ([]byte, error) {
	return json.Marshal(c)
}

// CredentialsFromJSON deserializes credentials from JSON.
func CredentialsFromJSON(data []byte) (*Credentials, error) {
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}
