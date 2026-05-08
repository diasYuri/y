package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultAnthropicAuthURL  = "https://platform.claude.com/v1/oauth/authorize"
	defaultAnthropicTokenURL = "https://platform.claude.com/v1/oauth/token"
	defaultAnthropicClientID = "y-cli"
)

// AnthropicLogin performs PKCE + loopback OAuth for Anthropic.
func AnthropicLogin(ctx context.Context) (*Credentials, error) {
	clientID := os.Getenv("ANTHROPIC_CLIENT_ID")
	if clientID == "" {
		clientID = defaultAnthropicClientID
	}

	verifier, err := GenerateCodeVerifier(128)
	if err != nil {
		return nil, err
	}
	challenge := CodeChallenge(verifier)
	state, err := GenerateCodeVerifier(32)
	if err != nil {
		return nil, err
	}

	lb, callbackURL, err := StartLoopbackServer()
	if err != nil {
		return nil, err
	}
	defer lb.Stop()

	authURL, err := buildAuthURL(defaultAnthropicAuthURL, clientID, callbackURL, challenge, state)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(os.Stderr, "Open this URL in your browser:\n%s\n", authURL)

	lbCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	res, err := lb.Wait(lbCtx)
	if err != nil {
		return nil, err
	}
	if res.Err != nil {
		return nil, res.Err
	}
	if res.State != state {
		return nil, fmt.Errorf("state mismatch")
	}

	return exchangeToken(ctx, defaultAnthropicTokenURL, clientID, callbackURL, res.Code, verifier)
}

func buildAuthURL(baseURL, clientID, redirectURI, challenge, state string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("scope", "openid profile")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func exchangeToken(ctx context.Context, tokenURL, clientID, redirectURI, code, verifier string) (*Credentials, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("client_id", clientID)
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("code_verifier", verifier)

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token,omitempty"`
		ExpiresIn    int    `json:"expires_in,omitempty"`
		TokenType    string `json:"token_type,omitempty"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, err
	}

	creds := &Credentials{
		ProviderID:   "anthropic",
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
	}
	if tokenResp.ExpiresIn > 0 {
		creds.ExpiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}
	return creds, nil
}

// AnthropicRefresh exchanges a refresh token for new credentials.
func AnthropicRefresh(ctx context.Context, refreshToken string) (*Credentials, error) {
	clientID := os.Getenv("ANTHROPIC_CLIENT_ID")
	if clientID == "" {
		clientID = defaultAnthropicClientID
	}

	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("client_id", clientID)
	data.Set("refresh_token", refreshToken)

	req, err := http.NewRequestWithContext(ctx, "POST", defaultAnthropicTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token,omitempty"`
		ExpiresIn    int    `json:"expires_in,omitempty"`
		TokenType    string `json:"token_type,omitempty"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, err
	}

	creds := &Credentials{
		ProviderID:   "anthropic",
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
	}
	if tokenResp.ExpiresIn > 0 {
		creds.ExpiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}
	return creds, nil
}
