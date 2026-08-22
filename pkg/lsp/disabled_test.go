//go:build !feature_lsp

package lsp

import (
	"context"
	"testing"
)

func TestUnavailableClient(t *testing.T) {
	client := NewClient(nil, nil)
	if _, err := client.Initialize(context.Background(), ""); err != ErrUnavailable {
		t.Fatalf("Initialize error = %v, want ErrUnavailable", err)
	}
}
