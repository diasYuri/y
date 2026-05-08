package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/yuri/y/internal/auth"
	"github.com/yuri/y/internal/feature"
	"github.com/yuri/y/pkg/agent"
)

func defaultHeadlessProviderFactory(_ context.Context, compiled *feature.Registry, opts headlessOptions) (agent.Provider, error) {
	if isOfflineMode() {
		return nil, newHeadlessError(exitCodeConfig, fmt.Errorf("offline mode is enabled (Y_OFFLINE); network requests are blocked"))
	}
	name := strings.TrimSpace(opts.providerName)
	if name == "" {
		name = detectHeadlessProvider(compiled)
	}
	if name == "" {
		return nil, newHeadlessError(exitCodeConfig, fmt.Errorf("no provider configured; set --provider or a provider API key environment variable"))
	}

	switch name {
	case "anthropic":
		if !providerHasKey(opts, "ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY") {
			if key, _ := authKeyOrError("anthropic"); key != "" {
				return newAnthropicProvider(headlessOptions{apiKey: key})
			}
			return nil, newHeadlessError(exitCodeConfig, fmt.Errorf("anthropic API key is required; set ANTHROPIC_OAUTH_TOKEN or ANTHROPIC_API_KEY, or pass --api-key, or run `y auth login anthropic`"))
		}
		return newAnthropicProvider(opts)
	case "google":
		if !providerHasKey(opts, "GEMINI_API_KEY") {
			if key, _ := authKeyOrError("google"); key != "" {
				return newGoogleProvider(headlessOptions{apiKey: key})
			}
			return nil, newHeadlessError(exitCodeConfig, fmt.Errorf("google API key is required; set GEMINI_API_KEY or pass --api-key, or run `y auth login google`"))
		}
		return newGoogleProvider(opts)
	case "local":
		if !providerHasKey(opts, "OPENAI_COMPATIBLE_API_KEY", "Y_OPENAI_COMPATIBLE_API_KEY") && !localProviderEnvConfigured() && strings.TrimSpace(opts.apiKey) == "" {
			return nil, newHeadlessError(exitCodeConfig, fmt.Errorf("openai-compatible API key is required; set OPENAI_COMPATIBLE_API_KEY, Y_OPENAI_COMPATIBLE_API_KEY, or pass --api-key"))
		}
		return newLocalProvider(opts)
	case "openai":
		if !providerHasKey(opts, "OPENAI_API_KEY") {
			if key, _ := authKeyOrError("openai"); key != "" {
				return newOpenAIProvider(headlessOptions{apiKey: key})
			}
			return nil, newHeadlessError(exitCodeConfig, fmt.Errorf("openai API key is required; set OPENAI_API_KEY or pass --api-key, or run `y auth login openai`"))
		}
		return newOpenAIProvider(opts)
	default:
		return nil, newHeadlessError(exitCodeConfig, fmt.Errorf("unknown provider %q", name))
	}
}

func errProviderUnavailable(name string) error {
	return fmt.Errorf("provider %q is not compiled into this binary", name)
}

func detectHeadlessProvider(compiled *feature.Registry) string {
	switch {
	case providerCompiled(compiled, "openai") && os.Getenv("OPENAI_API_KEY") != "":
		return "openai"
	case providerCompiled(compiled, "anthropic") && (os.Getenv("ANTHROPIC_OAUTH_TOKEN") != "" || os.Getenv("ANTHROPIC_API_KEY") != ""):
		return "anthropic"
	case providerCompiled(compiled, "google") && os.Getenv("GEMINI_API_KEY") != "":
		return "google"
	case providerCompiled(compiled, "local") && localProviderEnvConfigured():
		return "local"
	default:
		return ""
	}
}

func providerCompiled(compiled *feature.Registry, provider string) bool {
	return compiled != nil && compiled.Has(feature.KindProvider, provider)
}

func providerHasKey(opts headlessOptions, envNames ...string) bool {
	if strings.TrimSpace(opts.apiKey) != "" {
		return true
	}
	for _, name := range envNames {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

func localProviderEnvConfigured() bool {
	if os.Getenv("OPENAI_COMPATIBLE_API_KEY") != "" || os.Getenv("Y_OPENAI_COMPATIBLE_API_KEY") != "" {
		return true
	}
	return strings.EqualFold(os.Getenv("Y_OPENAI_COMPATIBLE_ALLOW_EMPTY_KEY"), "true")
}

func authKeyOrError(providerID string) (string, error) {
	key, err := auth.GetAPIKey(context.Background(), providerID)
	if err != nil {
		return "", err
	}
	return key, nil
}

func isOfflineMode() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("Y_OFFLINE")))
	switch v {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
