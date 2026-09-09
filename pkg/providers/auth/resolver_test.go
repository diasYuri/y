package auth

import (
	"context"
	"testing"
	"time"
)

type testRefresher struct{ calls int }

func (r *testRefresher) Refresh(_ context.Context, credential Credential) (Credential, error) {
	r.calls++
	credential.AccessToken = "fresh"
	credential.ExpiresAt = time.Now().Add(time.Hour)
	return credential, nil
}

func TestResolverUsesTenantCredentialAndRefreshesIt(t *testing.T) {
	store := NewInMemoryCredentialStore()
	if err := store.Put(context.Background(), Credential{ProviderID: "openai", TenantID: "tenant-a", AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	refresher := &testRefresher{}
	resolver := NewResolver(WithCredentialStore(store), WithTokenRefresher(refresher), WithCredentialSource(&StaticSource{}))
	credential, err := resolver.Resolve(context.Background(), ResolveRequest{ProviderID: "openai", TenantID: "tenant-a"})
	if err != nil || credential.Secret() != "fresh" || refresher.calls != 1 {
		t.Fatalf("credential = %#v, calls=%d, err=%v", credential, refresher.calls, err)
	}
	other, err := resolver.Resolve(context.Background(), ResolveRequest{ProviderID: "openai", TenantID: "tenant-b"})
	if err != nil {
		t.Fatal(err)
	}
	if other.Secret() != "" {
		t.Fatalf("tenant credential leaked: %#v", other)
	}
}

func TestResolverExplicitKeyHasPriority(t *testing.T) {
	store := NewInMemoryCredentialStore()
	_ = store.Put(context.Background(), Credential{ProviderID: "p", TenantID: "t", APIKey: "tenant-key"})
	resolver := NewResolver(WithCredentialStore(store))
	credential, err := resolver.Resolve(context.Background(), ResolveRequest{ProviderID: "p", TenantID: "t", APIKey: "request-key"})
	if err != nil || credential.Secret() != "request-key" {
		t.Fatalf("credential = %#v, err=%v", credential, err)
	}
}
