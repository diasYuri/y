// Package auth defines a small, public surface for resolving provider API
// keys. It is consumed by every concrete provider in pkg/providers so that all
// providers honour the same precedence rules:
//
//  1. Per-request override (StreamOptions.APIKey) — wins if non-empty.
//  2. Constructor APIKey (e.g. WithAPIKey) — wins next.
//  3. Source.Resolve(ctx, providerID) — the configured Source. By default this
//     is EnvSource, which reads from os.Getenv via a configurable lookup
//     function. Production deployments may swap this for an OAuth-backed
//     Source that consults the credential store.
package auth

import (
	"context"
	"os"
	"strings"
)

// Source resolves an API key for a provider. Implementations must be
// goroutine-safe.
type Source interface {
	// Resolve returns the API key (or OAuth bearer token, prefixed with
	// "Bearer ") for the given provider, or an empty string if no credential
	// is available. An error is returned only for unrecoverable failures (a
	// missing credential should return "" with a nil error).
	Resolve(ctx context.Context, providerID string) (string, error)
}

// LookupFunc is a function that returns the value of an environment variable.
type LookupFunc func(string) string

// EnvSource resolves credentials from environment variables. The mapping from
// provider ID to env var names is specific to each provider; the canonical
// list is documented on each provider package.
type EnvSource struct {
	// Lookup is the environment lookup function. If nil, os.Getenv is used.
	Lookup LookupFunc
}

// NewEnvSource returns an EnvSource using os.Getenv.
func NewEnvSource() *EnvSource {
	return &EnvSource{Lookup: os.Getenv}
}

// WithLookup returns a copy of the source with the given lookup function.
// A nil function falls back to os.Getenv.
func (e *EnvSource) WithLookup(lookup LookupFunc) *EnvSource {
	out := &EnvSource{Lookup: lookup}
	if out.Lookup == nil {
		out.Lookup = os.Getenv
	}
	return out
}

// Get returns the first non-empty value among the supplied environment
// variable names. The OAuth bearer convention ("Bearer <token>") is handled
// by callers; this helper returns the raw value.
func (e *EnvSource) Get(names ...string) string {
	lookup := e.Lookup
	if lookup == nil {
		lookup = os.Getenv
	}
	for _, name := range names {
		if value := strings.TrimSpace(lookup(name)); value != "" {
			return value
		}
	}
	return ""
}

// Resolve implements Source. EnvSource does not have provider-specific
// knowledge by itself; callers should use Get directly with the provider's
// list of env var names.
func (e *EnvSource) Resolve(ctx context.Context, providerID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	switch providerID {
	case "anthropic":
		if token := e.Get("ANTHROPIC_OAUTH_TOKEN"); token != "" {
			return "Bearer " + token, nil
		}
		return e.Get("ANTHROPIC_API_KEY"), nil
	case "openai":
		return e.Get("OPENAI_API_KEY"), nil
	case "google":
		return e.Get("GEMINI_API_KEY", "GOOGLE_API_KEY"), nil
	case "openai-compatible":
		return e.Get("OPENAI_COMPATIBLE_API_KEY", "Y_OPENAI_COMPATIBLE_API_KEY"), nil
	}
	return "", nil
}

// StaticSource always returns the same key. Useful in tests.
type StaticSource struct {
	Key string
}

// Resolve implements Source.
func (s *StaticSource) Resolve(ctx context.Context, providerID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.Key, nil
}
