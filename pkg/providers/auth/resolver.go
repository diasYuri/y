package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Credential is the provider credential resolved for one request. It is
// deliberately separate from provider request structs so callers can keep
// secrets out of snapshots and event payloads.
type Credential struct {
	ProviderID   string    `json:"provider_id"`
	TenantID     string    `json:"tenant_id,omitempty"`
	APIKey       string    `json:"-"`
	AccessToken  string    `json:"-"`
	RefreshToken string    `json:"-"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

func (c Credential) Secret() string {
	if c.AccessToken != "" {
		if c.TokenType != "" && !strings.EqualFold(c.TokenType, "bearer") {
			return c.TokenType + " " + c.AccessToken
		}
		return c.AccessToken
	}
	return c.APIKey
}

func (c Credential) Expired(now time.Time) bool {
	return !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt.Add(-time.Minute))
}

// CredentialStore stores credentials under both tenant and provider. A store
// must never include its secret values in snapshots, logs, or audit events.
type CredentialStore interface {
	Get(context.Context, string, string) (Credential, error)
	Put(context.Context, Credential) error
	Delete(context.Context, string, string) error
	List(context.Context, string) ([]Credential, error)
}

// SecretManager is the minimal adapter for an external secret manager.
type SecretManager interface {
	Resolve(context.Context, string, string) (string, error)
}

// TokenRefresher refreshes an expired OAuth credential. Implementations may
// call a provider-specific OAuth endpoint while Resolver serialises refreshes.
type TokenRefresher interface {
	Refresh(context.Context, Credential) (Credential, error)
}

// ResolveRequest describes the credential priority for a request.
type ResolveRequest struct {
	ProviderID string
	TenantID   string
	APIKey     string
	SecretName string
}

// Resolver applies the deterministic priority: explicit request key,
// tenant credential store, secret manager, then configured source (normally
// environment). Expired credentials are refreshed once under a per-key lock.
type Resolver struct {
	Store     CredentialStore
	Secrets   SecretManager
	Source    Source
	Refresher TokenRefresher
	Now       func() time.Time

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex
}

func NewResolver(options ...ResolverOption) *Resolver {
	r := &Resolver{Source: NewEnvSource(), locks: make(map[string]*sync.Mutex)}
	for _, option := range options {
		if option != nil {
			option(r)
		}
	}
	return r
}

type ResolverOption func(*Resolver)

func WithCredentialStore(store CredentialStore) ResolverOption {
	return func(r *Resolver) { r.Store = store }
}
func WithSecretManager(manager SecretManager) ResolverOption {
	return func(r *Resolver) { r.Secrets = manager }
}
func WithCredentialSource(source Source) ResolverOption {
	return func(r *Resolver) { r.Source = source }
}
func WithTokenRefresher(refresher TokenRefresher) ResolverOption {
	return func(r *Resolver) { r.Refresher = refresher }
}
func WithCredentialClock(now func() time.Time) ResolverOption {
	return func(r *Resolver) { r.Now = now }
}

func (r *Resolver) Resolve(ctx context.Context, request ResolveRequest) (Credential, error) {
	if err := contextErr(ctx); err != nil {
		return Credential{}, err
	}
	if strings.TrimSpace(request.ProviderID) == "" {
		return Credential{}, errors.New("provider ID is required")
	}
	if strings.TrimSpace(request.APIKey) != "" {
		return Credential{ProviderID: request.ProviderID, TenantID: request.TenantID, APIKey: request.APIKey}, nil
	}
	key := request.TenantID + "\x00" + request.ProviderID
	if r.Store != nil {
		credential, err := r.Store.Get(ctx, request.TenantID, request.ProviderID)
		if err != nil && !errors.Is(err, ErrCredentialNotFound) {
			return Credential{}, fmt.Errorf("load credential: %w", err)
		}
		if err == nil && credential.Secret() != "" {
			if credential.Expired(r.clock()) {
				if r.Refresher == nil || credential.RefreshToken == "" {
					return Credential{}, fmt.Errorf("credential for %s is expired", request.ProviderID)
				}
				lock := r.lockFor(key)
				lock.Lock()
				defer lock.Unlock()
				// Another request may have refreshed while this request waited.
				latest, latestErr := r.Store.Get(ctx, request.TenantID, request.ProviderID)
				if latestErr == nil && !latest.Expired(r.clock()) {
					return latest, nil
				}
				refreshed, refreshErr := r.Refresher.Refresh(ctx, credential)
				if refreshErr != nil {
					return Credential{}, fmt.Errorf("refresh credential for %s: %w", request.ProviderID, refreshErr)
				}
				refreshed.ProviderID = request.ProviderID
				refreshed.TenantID = request.TenantID
				if err := r.Store.Put(ctx, refreshed); err != nil {
					return Credential{}, fmt.Errorf("persist refreshed credential: %w", err)
				}
				return refreshed, nil
			}
			return credential, nil
		}
	}
	if r.Secrets != nil {
		name := request.SecretName
		if name == "" {
			name = request.ProviderID
		}
		secret, err := r.Secrets.Resolve(ctx, request.TenantID, name)
		if err != nil {
			if !errors.Is(err, ErrCredentialNotFound) {
				return Credential{}, fmt.Errorf("resolve secret: %w", err)
			}
		} else if strings.TrimSpace(secret) != "" {
			return Credential{ProviderID: request.ProviderID, TenantID: request.TenantID, APIKey: secret}, nil
		}
	}
	if r.Source != nil {
		secret, err := r.Source.Resolve(ctx, request.ProviderID)
		if err != nil {
			return Credential{}, err
		}
		if strings.TrimSpace(secret) != "" {
			return Credential{ProviderID: request.ProviderID, TenantID: request.TenantID, APIKey: secret}, nil
		}
	}
	return Credential{ProviderID: request.ProviderID, TenantID: request.TenantID}, nil
}

// ResolveAPIKey is a convenience for providers that only need the resolved
// wire credential.
func (r *Resolver) ResolveAPIKey(ctx context.Context, request ResolveRequest) (string, error) {
	credential, err := r.Resolve(ctx, request)
	if err != nil {
		return "", err
	}
	return credential.Secret(), nil
}

func (r *Resolver) clock() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *Resolver) lockFor(key string) *sync.Mutex {
	r.locksMu.Lock()
	defer r.locksMu.Unlock()
	if r.locks == nil {
		r.locks = make(map[string]*sync.Mutex)
	}
	if r.locks[key] == nil {
		r.locks[key] = &sync.Mutex{}
	}
	return r.locks[key]
}

var ErrCredentialNotFound = errors.New("credential not found")

// InMemoryCredentialStore is a tenant-isolated credential store intended for
// tests and examples. Values are copied on both ingress and egress.
type InMemoryCredentialStore struct {
	mu    sync.RWMutex
	items map[string]Credential
}

func NewInMemoryCredentialStore() *InMemoryCredentialStore {
	return &InMemoryCredentialStore{items: make(map[string]Credential)}
}

func (s *InMemoryCredentialStore) Get(ctx context.Context, tenantID, providerID string) (Credential, error) {
	if err := contextErr(ctx); err != nil {
		return Credential{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	credential, ok := s.items[tenantID+"\x00"+providerID]
	if !ok {
		return Credential{}, ErrCredentialNotFound
	}
	return credential, nil
}

func (s *InMemoryCredentialStore) Put(ctx context.Context, credential Credential) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if credential.ProviderID == "" {
		return errors.New("credential provider ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = make(map[string]Credential)
	}
	s.items[credential.TenantID+"\x00"+credential.ProviderID] = credential
	return nil
}

func (s *InMemoryCredentialStore) Delete(ctx context.Context, tenantID, providerID string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, tenantID+"\x00"+providerID)
	return nil
}

func (s *InMemoryCredentialStore) List(ctx context.Context, tenantID string) ([]Credential, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Credential
	for _, credential := range s.items {
		if credential.TenantID == tenantID {
			out = append(out, credential)
		}
	}
	return out, nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
