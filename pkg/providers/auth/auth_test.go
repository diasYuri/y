package auth

import (
	"context"
	"testing"
)

func TestEnvSourceAnthropicPrefersOAuth(t *testing.T) {
	src := &EnvSource{Lookup: func(name string) string {
		switch name {
		case "ANTHROPIC_OAUTH_TOKEN":
			return "oauth-token"
		case "ANTHROPIC_API_KEY":
			return "api-key"
		}
		return ""
	}}
	got, err := src.Resolve(context.Background(), "anthropic")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "Bearer oauth-token" {
		t.Fatalf("Resolve = %q, want Bearer oauth-token", got)
	}
}

func TestEnvSourceGoogleFallsBackToGoogleAPIKey(t *testing.T) {
	src := &EnvSource{Lookup: func(name string) string {
		if name == "GOOGLE_API_KEY" {
			return "google-key"
		}
		return ""
	}}
	got, err := src.Resolve(context.Background(), "google")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "google-key" {
		t.Fatalf("Resolve = %q, want google-key", got)
	}
}

func TestEnvSourceUnknownReturnsEmpty(t *testing.T) {
	src := &EnvSource{Lookup: func(string) string { return "value" }}
	got, err := src.Resolve(context.Background(), "made-up-provider")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "" {
		t.Fatalf("Resolve = %q, want empty for unknown provider", got)
	}
}

func TestStaticSource(t *testing.T) {
	src := &StaticSource{Key: "abc"}
	got, err := src.Resolve(context.Background(), "anything")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "abc" {
		t.Fatalf("Resolve = %q, want abc", got)
	}
}
