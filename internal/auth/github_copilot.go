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
)

const (
	defaultGitHubDeviceCodeURL = "https://github.com/login/device/code"
	defaultGitHubTokenURL      = "https://github.com/login/oauth/access_token"
	defaultGitHubClientID      = "y-cli"
)

// GitHubCopilotLogin performs the device code flow for GitHub Copilot.
func GitHubCopilotLogin(ctx context.Context) (*Credentials, error) {
	clientID := os.Getenv("GITHUB_CLIENT_ID")
	if clientID == "" {
		clientID = defaultGitHubClientID
	}

	deviceCodeURL := os.Getenv("GITHUB_DEVICE_CODE_URL")
	if deviceCodeURL == "" {
		deviceCodeURL = defaultGitHubDeviceCodeURL
	}
	tokenURL := os.Getenv("GITHUB_TOKEN_URL")
	if tokenURL == "" {
		tokenURL = defaultGitHubTokenURL
	}

	data := url.Values{}
	data.Set("client_id", clientID)
	data.Set("scope", "read:user")

	req, err := http.NewRequestWithContext(ctx, "POST", deviceCodeURL, strings.NewReader(data.Encode()))
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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code endpoint %d: %s", resp.StatusCode, string(body))
	}

	var devResp DeviceCodeResponse
	if err := json.Unmarshal(body, &devResp); err != nil {
		return nil, err
	}

	fmt.Fprintf(os.Stderr, "Enter this code in your browser: %s\n", devResp.UserCode)
	fmt.Fprintf(os.Stderr, "Or open: %s\n", devResp.VerificationURIComplete)
	if devResp.VerificationURIComplete == "" {
		fmt.Fprintf(os.Stderr, "Open: %s\n", devResp.VerificationURI)
	}

	creds, err := PollDeviceToken(ctx, tokenURL, clientID, devResp.DeviceCode, devResp.Interval)
	if err != nil {
		return nil, err
	}
	creds.ProviderID = "github_copilot"
	return creds, nil
}
