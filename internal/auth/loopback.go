package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// CallbackResult holds the OAuth callback parameters.
type CallbackResult struct {
	Code  string
	State string
	Err   error
}

// LoopbackServer listens on a local port for an OAuth callback.
type LoopbackServer struct {
	listener net.Listener
	server   *http.Server
	result   chan CallbackResult
}

// StartLoopbackServer starts a callback server on a random port.
// The returned URL is the callback endpoint (e.g. http://localhost:12345/callback).
func StartLoopbackServer() (*LoopbackServer, string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}

	lb := &LoopbackServer{
		listener: l,
		result:   make(chan CallbackResult, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", lb.handleCallback)
	lb.server = &http.Server{
		Handler:     mux,
		ReadTimeout: 5 * time.Second,
	}

	go lb.server.Serve(l)

	addr := l.Addr().String()
	callbackURL := fmt.Sprintf("http://%s/callback", addr)
	return lb, callbackURL, nil
}

// StartLoopbackServerOnPort starts a callback server on a specific port.
func StartLoopbackServerOnPort(port string) (*LoopbackServer, string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		return nil, "", err
	}

	lb := &LoopbackServer{
		listener: l,
		result:   make(chan CallbackResult, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", lb.handleCallback)
	lb.server = &http.Server{
		Handler:     mux,
		ReadTimeout: 5 * time.Second,
	}

	go lb.server.Serve(l)

	addr := l.Addr().String()
	callbackURL := fmt.Sprintf("http://%s/callback", addr)
	return lb, callbackURL, nil
}

func (lb *LoopbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	errMsg := r.URL.Query().Get("error")

	if errMsg != "" {
		lb.result <- CallbackResult{Err: fmt.Errorf("oauth error: %s", errMsg)}
		http.Error(w, "authorization failed", http.StatusBadRequest)
		return
	}

	if code == "" {
		lb.result <- CallbackResult{Err: fmt.Errorf("missing code")}
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	lb.result <- CallbackResult{Code: code, State: state}
	fmt.Fprintln(w, "Authorization successful. You may close this window.")
}

// Wait blocks until a callback arrives or the context is canceled.
func (lb *LoopbackServer) Wait(ctx context.Context) (CallbackResult, error) {
	select {
	case <-ctx.Done():
		lb.Stop()
		return CallbackResult{}, ctx.Err()
	case res := <-lb.result:
		lb.Stop()
		return res, nil
	}
}

// Stop shuts down the server.
func (lb *LoopbackServer) Stop() {
	if lb.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = lb.server.Shutdown(ctx)
	}
}

// RedirectURI returns the redirect_uri value for a listener address.
func RedirectURI(addr net.Addr) string {
	u := url.URL{
		Scheme: "http",
		Host:   addr.String(),
		Path:   "/callback",
	}
	return u.String()
}
