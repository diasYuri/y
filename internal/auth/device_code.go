package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeviceCodeResponse holds the initial device code response.
type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// PollDeviceToken polls the token endpoint until success, error, or context cancellation.
func PollDeviceToken(ctx context.Context, tokenURL, clientID string, deviceCode string, intervalSecs int) (*Credentials, error) {
	interval := time.Duration(intervalSecs) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}

		data := url.Values{}
		data.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		data.Set("device_code", deviceCode)
		data.Set("client_id", clientID)

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
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := resp.Header.Get("Retry-After")
			if retryAfter != "" {
				var secs int
				fmt.Sscanf(retryAfter, "%d", &secs)
				if secs > 0 {
					time.Sleep(time.Duration(secs) * time.Second)
				}
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			var errResp struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(body, &errResp) == nil {
				switch errResp.Error {
				case "authorization_pending", "pending":
					continue
				case "slow_down":
					interval += 5 * time.Second
					continue
				case "expired_token":
					return nil, fmt.Errorf("device code expired")
				case "access_denied":
					return nil, fmt.Errorf("access denied")
				default:
					return nil, fmt.Errorf("token error: %s", errResp.Error)
				}
			}
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
			AccessToken:  tokenResp.AccessToken,
			RefreshToken: tokenResp.RefreshToken,
			TokenType:    tokenResp.TokenType,
		}
		if tokenResp.ExpiresIn > 0 {
			creds.ExpiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		}
		return creds, nil
	}
}
