package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/diasYuri/y/pkg/providers/auth"
)

func (c Client) authorizationCode(ctx context.Context) (auth.Credential, error) {
	address := c.Config.ListenAddress
	if address == "" {
		address = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return auth.Credential{}, errors.New("oauth: callback must listen on loopback")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return auth.Credential{}, err
	}
	defer func() { _ = listener.Close() }()
	redirect := "http://" + listener.Addr().String() + "/callback"
	if c.Config.RedirectURI != "" {
		redirect = c.Config.RedirectURI
	}
	callback, err := url.Parse(redirect)
	if err != nil || callback.Scheme != "http" || net.ParseIP(callback.Hostname()) == nil || !net.ParseIP(callback.Hostname()).IsLoopback() {
		return auth.Credential{}, errors.New("oauth: redirect must use loopback HTTP")
	}
	verifier := rand.Text() + rand.Text()
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := rand.Text()
	result := make(chan string, 1)
	denied := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(callback.Path, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("error") != "" {
			select {
			case denied <- struct{}{}:
			default:
			}
			http.Error(w, "Authorization denied", http.StatusForbidden)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
			return
		}
		select {
		case result <- code:
		default:
		}
		w.WriteHeader(http.StatusOK)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	served := make(chan struct{})
	go func() { defer close(served); _ = server.Serve(listener) }()
	defer func() { _ = server.Close(); <-served }()
	u, err := endpointURL(c.Config.AuthorizationURL)
	if err != nil {
		return auth.Credential{}, err
	}

	q := u.Query()
	for key, values := range c.Config.AuthorizationParams {
		q[key] = append([]string(nil), values...)
	}
	q.Set("client_id", c.Config.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirect)
	q.Set("scope", strings.Join(c.Config.Scopes, " "))
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	u.RawQuery = q.Encode()
	if err := c.Notify(ctx, Notice{URL: u.String()}); err != nil {
		return auth.Credential{}, err
	}
	select {
	case <-ctx.Done():
		return auth.Credential{}, ctx.Err()
	case <-denied:
		return auth.Credential{}, errors.New("oauth: access denied")
	case code := <-result:
		return c.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect}})
	}
}
