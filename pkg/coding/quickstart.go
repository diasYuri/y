package coding

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/providers"
	"github.com/diasYuri/y/pkg/providers/anthropic"
	"github.com/diasYuri/y/pkg/providers/auth"
	"github.com/diasYuri/y/pkg/providers/google"
	"github.com/diasYuri/y/pkg/providers/openai"
	"github.com/diasYuri/y/pkg/providers/openai_compatible"
	"github.com/diasYuri/y/pkg/tools"
	"github.com/diasYuri/y/pkg/tools/filesystem"
	"github.com/diasYuri/y/pkg/tools/git"
	"github.com/diasYuri/y/pkg/tools/shell"
)

// AgentConfig is a declarative quickstart configuration for a coding agent.
// It wires a provider, a model, the built-in tool families and the agent
// loop without requiring the caller to assemble each piece.
type AgentConfig struct {
	// ProviderID selects the provider implementation: "openai",
	// "anthropic", "google" or "openai-compatible". Required.
	ProviderID string
	// ModelID selects the model. When empty, the first model reported by
	// the provider is used.
	ModelID string
	// APIKey overrides the credential. When empty, the provider resolves
	// its canonical environment variables (see ProviderEnvVars).
	APIKey string
	// BaseURL overrides the provider endpoint. Optional, except for
	// "openai-compatible", where it is required.
	BaseURL string
	// WorkspaceRoot scopes the built-in tools. Required; must exist.
	WorkspaceRoot string
	// SystemPrompt overrides the default coding system prompt
	// (SystemPrompt). Optional.
	SystemPrompt string
	// MaxTurns bounds the agent loop. Defaults to the agent default (32).
	MaxTurns int
	// Tools lists the built-in tool families to register: "filesystem",
	// "shell", "git". Empty means all three.
	Tools []string
	// Policy authorizes tool calls. Defaults to tools.WorkspacePolicy().
	Policy tools.Policy
}

// ProviderEnvVars returns the canonical environment variables consulted for
// a provider's credential, in lookup order. It is exported so callers can
// surface actionable configuration errors.
func ProviderEnvVars(providerID string) []string {
	switch providerID {
	case "anthropic":
		return []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}
	case "openai":
		return []string{"OPENAI_API_KEY"}
	case "google":
		return []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}
	case "openai-compatible":
		return []string{"OPENAI_COMPATIBLE_API_KEY", "Y_OPENAI_COMPATIBLE_API_KEY"}
	}
	return nil
}

// SupportedProviders lists the provider IDs accepted by AgentConfig.
func SupportedProviders() []string {
	return []string{"anthropic", "openai", "google", "openai-compatible"}
}

// NewAgent assembles a coding agent from cfg: provider (with credential
// resolution from the environment), model catalog lookup, built-in tools
// scoped to the workspace, and the agent loop. Extra opts are applied last.
func NewAgent(ctx context.Context, cfg AgentConfig, opts ...agent.Option) (*agent.Agent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	provider, providerID, err := newQuickstartProvider(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.WorkspaceRoot == "" {
		return nil, errors.New("coding: WorkspaceRoot is required")
	}
	root, err := resolveWorkspaceRoot(cfg.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	model, err := resolveQuickstartModel(ctx, provider, providerID, cfg)
	if err != nil {
		return nil, err
	}
	families := quickstartToolFamilies(cfg.Tools)
	registry, err := newQuickstartRegistry(cfg, root)
	if err != nil {
		return nil, err
	}

	systemPrompt := cfg.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = systemPromptForToolFamilies(families)
	}
	agentOpts := []agent.Option{
		agent.WithModel(model),
		agent.WithSystemPrompt(systemPrompt),
		agent.WithWorkspaceRoot(root),
	}
	if cfg.MaxTurns > 0 {
		agentOpts = append(agentOpts, agent.WithMaxTurns(cfg.MaxTurns))
	}
	agentOpts = append(agentOpts, opts...)
	return agent.New(provider, registry, agentOpts...), nil
}

func newQuickstartProvider(cfg AgentConfig) (providers.Provider, ai.ProviderID, error) {
	providerID := ai.ProviderID(cfg.ProviderID)
	switch providerID {
	case "openai":
		pOpts := []openai.Option{}
		if cfg.APIKey != "" {
			pOpts = append(pOpts, openai.WithAPIKey(cfg.APIKey))
		}
		if cfg.BaseURL != "" {
			pOpts = append(pOpts, openai.WithBaseURL(cfg.BaseURL))
		}
		return openai.New(pOpts...), providerID, nil
	case "anthropic":
		pOpts := []anthropic.Option{}
		if cfg.APIKey != "" {
			pOpts = append(pOpts, anthropic.WithAPIKey(cfg.APIKey))
		}
		if cfg.BaseURL != "" {
			pOpts = append(pOpts, anthropic.WithBaseURL(cfg.BaseURL))
		}
		return anthropic.New(pOpts...), providerID, nil
	case "google":
		pOpts := []google.Option{}
		if cfg.APIKey != "" {
			pOpts = append(pOpts, google.WithAPIKey(cfg.APIKey))
		}
		if cfg.BaseURL != "" {
			pOpts = append(pOpts, google.WithBaseURL(cfg.BaseURL))
		}
		return google.New(pOpts...), providerID, nil
	case "openai-compatible":
		if cfg.BaseURL == "" {
			return nil, "", errors.New("coding: BaseURL is required for the openai-compatible provider")
		}
		cOpts := []openai_compatible.Option{openai_compatible.WithBaseURL(cfg.BaseURL)}
		if cfg.APIKey != "" {
			cOpts = append(cOpts, openai_compatible.WithAPIKey(cfg.APIKey))
		}
		return openai_compatible.New(cOpts...), providerID, nil
	case "":
		return nil, "", fmt.Errorf("coding: ProviderID is required (one of %s)", strings.Join(SupportedProviders(), ", "))
	}
	return nil, "", fmt.Errorf("coding: unsupported provider %q (one of %s)", cfg.ProviderID, strings.Join(SupportedProviders(), ", "))
}

// resolveQuickstartCredential verifies a credential exists for the provider.
// The only credential-free exception is an openai-compatible endpoint that
// explicitly opts into empty authentication. The check mirrors the
// precedence documented in pkg/providers/auth.
func resolveQuickstartCredential(ctx context.Context, cfg AgentConfig) error {
	if cfg.APIKey != "" || compatibleEndpointAllowsEmptyAuth(cfg) {
		return nil
	}
	resolver := auth.NewResolver()
	credential, err := resolver.Resolve(ctx, auth.ResolveRequest{ProviderID: cfg.ProviderID})
	if err != nil {
		return fmt.Errorf("coding: resolve credential for %q: %w", cfg.ProviderID, err)
	}
	if credential.Secret() == "" {
		names := ProviderEnvVars(cfg.ProviderID)
		if len(names) == 0 {
			return fmt.Errorf("coding: no credential found for provider %q", cfg.ProviderID)
		}
		return fmt.Errorf("coding: no credential found for provider %q: set one of %s or pass AgentConfig.APIKey", cfg.ProviderID, strings.Join(names, ", "))
	}
	return nil
}

func compatibleEndpointAllowsEmptyAuth(cfg AgentConfig) bool {
	return cfg.ProviderID == "openai-compatible" && cfg.BaseURL != "" &&
		strings.EqualFold(strings.TrimSpace(os.Getenv("Y_OPENAI_COMPATIBLE_ALLOW_EMPTY_KEY")), "true")
}

func resolveQuickstartModel(ctx context.Context, provider providers.Provider, providerID ai.ProviderID, cfg AgentConfig) (ai.Model, error) {
	if err := resolveQuickstartCredential(ctx, cfg); err != nil {
		return ai.Model{}, err
	}
	catalog := providers.NewCatalog()
	if err := catalog.Register(provider); err != nil {
		return ai.Model{}, fmt.Errorf("coding: register provider catalog: %w", err)
	}
	if cfg.ModelID != "" {
		model, err := catalog.Lookup(ctx, providerID, cfg.ModelID)
		if err != nil {
			if providerID == "openai-compatible" && errors.Is(err, providers.ErrModelNotFound) {
				caps := provider.Capabilities(cfg.ModelID)
				return ai.Model{
					ID:       cfg.ModelID,
					Name:     cfg.ModelID,
					API:      "openai-completions",
					Provider: providerID,
					BaseURL:  strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
					Input:    []ai.InputKind{ai.InputText},
					Capabilities: ai.ModelCapabilities{
						Vision:           caps.Vision,
						Tools:            caps.Tools,
						Reasoning:        caps.Reasoning,
						PromptCache:      caps.PromptCache,
						JSONMode:         caps.JSONMode,
						StructuredOutput: caps.StructuredOutput,
						Streaming:        caps.Streaming,
					},
				}, nil
			}
			return ai.Model{}, fmt.Errorf("coding: lookup model: %w", err)
		}
		return model, nil
	}
	models, err := catalog.List(ctx)
	if err != nil {
		return ai.Model{}, fmt.Errorf("coding: list models for %q: %w", providerID, err)
	}
	if len(models) == 0 {
		return ai.Model{}, fmt.Errorf("coding: provider %q reported no models", providerID)
	}
	return models[0], nil
}

func newQuickstartRegistry(cfg AgentConfig, root string) (*tools.Registry, error) {
	policy := cfg.Policy
	if policy == nil {
		policy = tools.WorkspacePolicy()
	}
	registry := tools.NewRegistry(tools.WithPolicy(policy))
	families := quickstartToolFamilies(cfg.Tools)
	for _, family := range families {
		var err error
		switch family {
		case "filesystem":
			err = filesystem.Register(registry, filesystem.Options{WorkspaceRoot: root, Policy: policy})
		case "shell":
			err = shell.Register(registry, shell.Options{WorkspaceRoot: root, Policy: policy})
		case "git":
			err = git.Register(registry, git.Options{WorkspaceRoot: root, Policy: policy})
		default:
			return nil, fmt.Errorf("coding: unknown tool family %q (one of filesystem, shell, git)", family)
		}
		if err != nil {
			return nil, fmt.Errorf("coding: register %s tools: %w", family, err)
		}
	}
	return registry, nil
}

func quickstartToolFamilies(selected []string) []string {
	if len(selected) == 0 {
		return []string{"filesystem", "shell", "git"}
	}
	return append([]string(nil), selected...)
}

func resolveWorkspaceRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("coding: resolve workspace root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("coding: workspace root %q: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("coding: workspace root %q is not a directory", abs)
	}
	return abs, nil
}
