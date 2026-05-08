package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestLoopbackCallbackExtraction(t *testing.T) {
	lb, url, err := StartLoopbackServer()
	if err != nil {
		t.Skipf("cannot bind loopback: %v", err)
	}
	defer lb.Stop()

	go func() {
		time.Sleep(100 * time.Millisecond)
		resp, err := http.Get(url + "?code=abc123&state=xyz")
		if err != nil {
			t.Logf("http get: %v", err)
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := lb.Wait(ctx)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if res.Err != nil {
		t.Fatalf("callback error: %v", res.Err)
	}
	if res.Code != "abc123" {
		t.Errorf("code: got %q, want %q", res.Code, "abc123")
	}
	if res.State != "xyz" {
		t.Errorf("state: got %q, want %q", res.State, "xyz")
	}
}

func TestLoopbackTimeout(t *testing.T) {
	lb, _, err := StartLoopbackServer()
	if err != nil {
		t.Skipf("cannot bind loopback: %v", err)
	}
	defer lb.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = lb.Wait(ctx)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func TestLoopbackErrorParam(t *testing.T) {
	lb, url, err := StartLoopbackServer()
	if err != nil {
		t.Skipf("cannot bind loopback: %v", err)
	}
	defer lb.Stop()

	go func() {
		time.Sleep(50 * time.Millisecond)
		resp, err := http.Get(url + "?error=access_denied")
		if err != nil {
			t.Logf("http get: %v", err)
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := lb.Wait(ctx)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if res.Err == nil {
		t.Fatal("expected oauth error")
	}
	want := "access_denied"
	if res.Err.Error() != fmt.Sprintf("oauth error: %s", want) {
		t.Errorf("error: got %q", res.Err.Error())
	}
}
