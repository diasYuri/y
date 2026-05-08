package auth

import (
	"context"
	"fmt"
)

// Login initiates OAuth login for the given provider.
func Login(ctx context.Context, providerID string) (*Credentials, error) {
	switch providerID {
	case "anthropic":
		return AnthropicLogin(ctx)
	case "openai":
		return OpenAILogin(ctx)
	case "google":
		return GoogleLogin(ctx)
	case "github_copilot":
		return GitHubCopilotLogin(ctx)
	default:
		return nil, fmt.Errorf("unknown provider %q", providerID)
	}
}

// GetAPIKey returns the current access token for a provider, refreshing if needed.
func GetAPIKey(ctx context.Context, providerID string) (string, error) {
	store := NewStore()
	creds, err := store.Read(providerID)
	if err != nil {
		return "", err
	}
	if creds == nil {
		return "", fmt.Errorf("no credentials for provider %q", providerID)
	}

	if !creds.IsExpired() {
		return creds.AccessToken, nil
	}

	if creds.RefreshToken == "" {
		return "", fmt.Errorf("token expired and no refresh token for %q", providerID)
	}

	refreshed, err := refreshToken(ctx, providerID, creds.RefreshToken)
	if err != nil {
		return "", err
	}
	refreshed.ProviderID = providerID
	if err := store.Write(refreshed); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

// Logout removes stored credentials for a provider.
func Logout(providerID string) error {
	store := NewStore()
	return store.Delete(providerID)
}

// ListProviders returns all providers with stored credentials.
func ListProviders() ([]string, error) {
	store := NewStore()
	return store.List()
}

func refreshToken(ctx context.Context, providerID, refreshToken string) (*Credentials, error) {
	switch providerID {
	case "anthropic":
		return AnthropicRefresh(ctx, refreshToken)
	case "openai":
		return OpenAIRefresh(ctx, refreshToken)
	case "google":
		return GoogleRefresh(ctx, refreshToken)
	default:
		return nil, fmt.Errorf("refresh not supported for provider %q", providerID)
	}
}
