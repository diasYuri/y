//go:build feature_lsp

package lsp

import (
	"testing"
)

func TestNewClient(t *testing.T) {
	c := NewClient(nil, nil)
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.pending == nil {
		t.Fatal("expected non-nil pending map")
	}
}

func TestResponseErrorString(t *testing.T) {
	err := &ResponseError{Code: -32600, Message: "invalid request"}
	if err.Message != "invalid request" {
		t.Fatalf("message = %q", err.Message)
	}
}
