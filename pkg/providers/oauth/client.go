// Package oauth implements reusable OAuth device and PKCE authorization flows.
// The host supplies endpoints, HTTP client and notifications. This package never
// reads a terminal, prints a URL, starts a browser or stores credentials.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/diasYuri/y/pkg/providers/auth"
)

type Config struct {
	ProviderID          string
	ClientID            string
	ClientSecret        string
	AuthorizationURL    string
	TokenURL            string
	DeviceURL           string
	Scopes              []string
	ListenAddress       string
	RedirectURI         string
	Timeout             time.Duration
	TokenJSON           bool
	AuthorizationParams url.Values
}
type Notice struct {
	URL       string    `json:"url"`
	UserCode  string    `json:"user_code,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}
type Client struct {
	Config Config
	HTTP   *http.Client
	Notify func(context.Context, Notice) error
	Now    func() time.Time
	Sleep  func(context.Context, time.Duration) error
}

func (c Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
func (c Client) wait(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (c Client) deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	d := c.Config.Timeout
	if d <= 0 {
		d = 5 * time.Minute
	}
	return context.WithTimeout(ctx, d)
}
func (c Client) Login(ctx context.Context) (auth.Credential, error) {
	ctx, cancel := c.deadline(ctx)
	defer cancel()
	if c.Config.ClientID == "" || c.Config.TokenURL == "" || c.Notify == nil {
		return auth.Credential{}, errors.New("oauth: client ID, token URL and notification callback are required")
	}
	if c.Config.DeviceURL != "" {
		return c.device(ctx)
	}
	return c.authorizationCode(ctx)
}
func (c Client) Refresh(ctx context.Context, credential auth.Credential) (auth.Credential, error) {
	if credential.RefreshToken == "" {
		return auth.Credential{}, errors.New("oauth: refresh token is required")
	}
	ctx, cancel := c.deadline(ctx)
	defer cancel()
	value, err := c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {credential.RefreshToken}})
	if err == nil && value.RefreshToken == "" {
		value.RefreshToken = credential.RefreshToken
	}
	value.TenantID = credential.TenantID
	return value, err
}

type endpointError struct {
	code   string
	status int
	retry  time.Duration
}

func (e *endpointError) Error() string {
	return fmt.Sprintf("oauth: endpoint rejected request (HTTP %d, %s)", e.status, e.code)
}
func (c Client) post(ctx context.Context, endpoint string, values url.Values, asJSON bool, result any) error {
	if _, err := endpointURL(endpoint); err != nil {
		return err
	}

	var body io.Reader = strings.NewReader(values.Encode())
	contentType := "application/x-www-form-urlencoded"
	if asJSON {
		fields := map[string]string{}
		for key := range values {
			fields[key] = values.Get(key)
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(raw))
		contentType = "application/json"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return errors.New("oauth: invalid endpoint request")
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 || next.URL.Scheme != request.URL.Scheme || next.URL.Host != request.URL.Host {
			return errors.New("oauth: redirect outside endpoint origin")
		}
		if client.CheckRedirect != nil {
			return client.CheckRedirect(next, via)
		}
		return nil
	}
	response, err := copyClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("oauth: HTTP request failed")
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return errors.New("oauth: invalid endpoint response")
	}
	var oauthError struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &oauthError)
	if response.StatusCode < 200 || response.StatusCode >= 300 || oauthError.Error != "" {
		code := "request_failed"
		for _, known := range []string{"authorization_pending", "pending", "slow_down", "expired_token", "access_denied", "invalid_grant", "invalid_client"} {
			if oauthError.Error == known {
				code = known
			}
		}
		retry := time.Duration(0)
		if secs, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && secs > 0 {
			retry = time.Duration(secs) * time.Second
		}
		return &endpointError{code: code, status: response.StatusCode, retry: retry}
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return errors.New("oauth: invalid JSON response")
	}
	return nil
}
func (c Client) token(ctx context.Context, values url.Values) (auth.Credential, error) {
	values.Set("client_id", c.Config.ClientID)
	if c.Config.ClientSecret != "" {
		values.Set("client_secret", c.Config.ClientSecret)
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := c.post(ctx, c.Config.TokenURL, values, c.Config.TokenJSON, &token); err != nil {
		return auth.Credential{}, err
	}
	if token.AccessToken == "" {
		return auth.Credential{}, errors.New("oauth: missing access token")
	}
	credential := auth.Credential{ProviderID: c.Config.ProviderID, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, TokenType: token.TokenType}
	if token.ExpiresIn > 0 {
		credential.ExpiresAt = c.now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	return credential, nil
}

func endpointURL(endpoint string) (*url.URL, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, errors.New("oauth: invalid endpoint URL")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if u.Scheme == "https" || u.Scheme == "http" && loopback {
		return u, nil
	}
	return nil, errors.New("oauth: endpoint requires HTTPS (HTTP is allowed only on loopback)")
}
