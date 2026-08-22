//go:build !feature_rpc

package rpc

import "testing"

func TestUnavailableServer(t *testing.T) {
	server := NewServer(ServerConfig{})
	if err := server.ListenAndServe(); err != ErrUnavailable {
		t.Fatalf("ListenAndServe error = %v, want ErrUnavailable", err)
	}
	if server.Addr() != nil {
		t.Fatal("unavailable server should not have an address")
	}
}
