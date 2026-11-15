package oauth_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/diasYuri/y/pkg/providers/auth"
	"github.com/diasYuri/y/pkg/providers/oauth"
)

func TestDeviceSlowDownRefreshAndSafeErrors(t *testing.T) {
	now := time.Unix(100, 0)
	polls := 0
	var waits []time.Duration
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.URL.Path == "/device" {
			_, _ = fmt.Fprint(w, `{"device_code":"private-device","user_code":"VISIBLE","verification_uri":"https://example.test/device","expires_in":60,"interval":1}`)
			return
		}
		if r.Form.Get("grant_type") == "refresh_token" {
			_, _ = fmt.Fprint(w, `{"access_token":"refreshed","expires_in":100}`)
			return
		}
		polls++
		switch polls {
		case 1:
			_, _ = fmt.Fprint(w, `{"error":"authorization_pending"}`)
		case 2:
			_, _ = fmt.Fprint(w, `{"error":"slow_down"}`)
		default:
			_, _ = fmt.Fprint(w, `{"access_token":"token","refresh_token":"refresh","expires_in":100}`)
		}
	}))
	defer server.Close()
	notified := false
	c := oauth.Client{Config: oauth.Config{ProviderID: "fixture", ClientID: "client", DeviceURL: server.URL + "/device", TokenURL: server.URL + "/token"}, HTTP: server.Client(), Now: func() time.Time { return now }, Sleep: func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		now = now.Add(d)
		return ctx.Err()
	}, Notify: func(_ context.Context, n oauth.Notice) error {
		notified = true
		if strings.Contains(n.URL, "private-device") || n.UserCode != "VISIBLE" {
			t.Fatal("bad notice")
		}
		return nil
	}}
	credential, err := c.Login(context.Background())
	if err != nil || !notified || credential.AccessToken != "token" || len(waits) != 3 || waits[2] != 6*time.Second {
		t.Fatalf("device flow: %#v %v", waits, err)
	}
	refreshed, err := c.Refresh(context.Background(), credential)
	if err != nil || refreshed.AccessToken != "refreshed" || refreshed.RefreshToken != "refresh" {
		t.Fatal("refresh rotation/preservation failed", err)
	}
}

func TestDeviceDenialExpiryAndCancel(t *testing.T) {
	for _, mode := range []string{"denied", "expired", "cancelled", "safe_error"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/device" {
					_, _ = fmt.Fprint(w, `{"device_code":"private","user_code":"CODE","verification_uri":"https://example.test","expires_in":2,"interval":1}`)
					return
				}
				w.WriteHeader(http.StatusBadRequest)
				if mode == "denied" {
					_, _ = fmt.Fprint(w, `{"error":"access_denied"}`)
				} else {
					_, _ = fmt.Fprint(w, `{"error":"private-token-sentinel"}`)
				}
			}))
			defer server.Close()
			now := time.Unix(100, 0)
			client := oauth.Client{Config: oauth.Config{ClientID: "client", DeviceURL: server.URL + "/device", TokenURL: server.URL + "/token"}, HTTP: server.Client(), Notify: func(context.Context, oauth.Notice) error { return nil }, Now: func() time.Time { return now }, Sleep: func(context.Context, time.Duration) error {
				if mode == "cancelled" {
					return context.Canceled
				}
				if mode == "expired" {
					now = now.Add(time.Minute)
				}
				return nil
			}}
			_, err := client.Login(context.Background())
			if err == nil || strings.Contains(err.Error(), "private-token-sentinel") {
				t.Fatal("unsafe error", err)
			}
		})
	}
}

func TestPKCECallbackStateAndExchange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("code_verifier") == "" || r.Form.Get("code") != "authorization-code" {
			t.Error("missing exchange binding")
		}
		_, _ = fmt.Fprint(w, `{"access_token":"token","expires_in":100}`)
	}))
	defer server.Close()
	client := oauth.Client{Config: oauth.Config{ProviderID: "fixture", ClientID: "client", AuthorizationURL: server.URL + "/authorize", TokenURL: server.URL}, HTTP: server.Client()}
	client.Notify = func(ctx context.Context, notice oauth.Notice) error {
		u, err := url.Parse(notice.URL)
		if err != nil {
			return err
		}
		q := u.Query()
		if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
			t.Fatal("missing PKCE")
		}
		callback, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			return err
		}
		values := callback.Query()
		values.Set("state", "wrong")
		values.Set("code", "authorization-code")
		callback.RawQuery = values.Encode()
		response, err := http.Get(callback.String())
		if err != nil {
			return err
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatal("CSRF state accepted")
		}
		values.Set("state", q.Get("state"))
		callback.RawQuery = values.Encode()
		response, err = http.Get(callback.String())
		if err != nil {
			return err
		}
		_ = response.Body.Close()
		return nil
	}
	credential, err := client.Login(context.Background())
	if err != nil || credential.AccessToken != "token" {
		t.Fatal("PKCE failed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client.Notify = func(context.Context, oauth.Notice) error { return nil }
	if _, err := client.Login(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel failed", err)
	}
	if _, err := client.Refresh(context.Background(), auth.Credential{}); err == nil {
		t.Fatal("accepted empty refresh token")
	}
}
