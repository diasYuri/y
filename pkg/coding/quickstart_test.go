package coding

import (
	"context"
	"strings"
	"testing"

	"github.com/diasYuri/y/pkg/providers/openai_compatible"
	"github.com/diasYuri/y/pkg/tools"
)

func TestNewAgent_Validation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cases := []struct {
		name string
		cfg  AgentConfig
		want string
	}{
		{
			name: "missing provider",
			cfg:  AgentConfig{WorkspaceRoot: t.TempDir()},
			want: "ProviderID is required",
		},
		{
			name: "unsupported provider",
			cfg:  AgentConfig{ProviderID: "bogus", WorkspaceRoot: t.TempDir()},
			want: "unsupported provider",
		},
		{
			name: "compatible requires base URL",
			cfg:  AgentConfig{ProviderID: "openai-compatible", WorkspaceRoot: t.TempDir()},
			want: "BaseURL is required",
		},
		{
			name: "missing workspace root",
			cfg:  AgentConfig{ProviderID: "openai-compatible", BaseURL: "http://localhost:1", APIKey: "k"},
			want: "WorkspaceRoot is required",
		},
		{
			name: "workspace root does not exist",
			cfg:  AgentConfig{ProviderID: "openai-compatible", BaseURL: "http://localhost:1", APIKey: "k", WorkspaceRoot: "/nonexistent-y-quickstart"},
			want: "workspace root",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewAgent(ctx, tc.cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestNewAgent_Defaults(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, err := NewAgent(context.Background(), AgentConfig{
		ProviderID:    "openai-compatible",
		BaseURL:       "http://localhost:1",
		APIKey:        "test-key",
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	if a == nil {
		t.Fatal("expected agent, got nil")
	}
	if SystemPrompt() == "" {
		t.Fatal("SystemPrompt must not be empty")
	}
}

func TestNewAgent_CompatibleCustomModel(t *testing.T) {
	t.Setenv("Y_OPENAI_COMPATIBLE_ALLOW_EMPTY_KEY", "true")
	a, err := NewAgent(context.Background(), AgentConfig{
		ProviderID:    "openai-compatible",
		ModelID:       "llama3",
		BaseURL:       "http://localhost:1/v1",
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewAgent with custom compatible model: %v", err)
	}
	if a == nil {
		t.Fatal("expected agent")
	}
}

func TestNewAgent_RequiresCredentialsForCustomEndpoints(t *testing.T) {
	cases := []struct {
		provider string
		envVars  []string
	}{
		{provider: "openai", envVars: []string{"OPENAI_API_KEY"}},
		{provider: "anthropic", envVars: []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}},
		{provider: "google", envVars: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}},
		{provider: "openai-compatible", envVars: []string{"OPENAI_COMPATIBLE_API_KEY", "Y_OPENAI_COMPATIBLE_API_KEY", "Y_OPENAI_COMPATIBLE_ALLOW_EMPTY_KEY"}},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			for _, name := range tc.envVars {
				t.Setenv(name, "")
			}
			_, err := NewAgent(context.Background(), AgentConfig{
				ProviderID:    tc.provider,
				BaseURL:       "http://localhost:1/v1",
				WorkspaceRoot: t.TempDir(),
			})
			if err == nil || !strings.Contains(err.Error(), "no credential found") {
				t.Fatalf("expected missing credential error, got %v", err)
			}
		})
	}
}

func TestSystemPromptForToolFamilies(t *testing.T) {
	t.Parallel()
	prompt := systemPromptForToolFamilies([]string{"git"})
	for _, name := range []string{"git_status", "git_diff", "git_log", "git_branch", "git_checkout", "git_commit"} {
		if !strings.Contains(prompt, name) {
			t.Errorf("git-only prompt missing %q", name)
		}
	}
	for _, name := range []string{"read_file", "write_file", "list_files", "search", "edit", "patch", "run_command"} {
		if strings.Contains(prompt, name) {
			t.Errorf("git-only prompt advertises unregistered tool %q", name)
		}
	}
}

func TestNewAgent_ToolFamilies(t *testing.T) {
	t.Parallel()
	base := AgentConfig{
		ProviderID:    "openai-compatible",
		BaseURL:       "http://localhost:1",
		APIKey:        "test-key",
		WorkspaceRoot: t.TempDir(),
	}

	cfg := base
	cfg.Tools = []string{"git"}
	a, err := NewAgent(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewAgent with git family: %v", err)
	}
	if a == nil {
		t.Fatal("expected agent")
	}

	cfg = base
	cfg.Tools = []string{"bogus"}
	if _, err := NewAgent(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "unknown tool family") {
		t.Fatalf("expected unknown tool family error, got %v", err)
	}
}

func TestProviderEnvVars(t *testing.T) {
	t.Parallel()
	if got := ProviderEnvVars("openai"); len(got) == 0 || got[0] != "OPENAI_API_KEY" {
		t.Fatalf("unexpected env vars for openai: %v", got)
	}
	if got := ProviderEnvVars("bogus"); got != nil {
		t.Fatalf("expected nil for unknown provider, got %v", got)
	}
}

func TestQuickstartRegistry_UsesInjectedPolicy(t *testing.T) {
	t.Parallel()
	policy := tools.PolicyFunc(func(ctx context.Context, req tools.PolicyRequest) (tools.PolicyDecision, error) {
		return tools.PolicyDecision{Kind: tools.DecisionDeny, Reason: "test deny"}, nil
	})
	cfg := AgentConfig{
		ProviderID:    "openai-compatible",
		BaseURL:       "http://localhost:1",
		APIKey:        "test-key",
		WorkspaceRoot: t.TempDir(),
		Policy:        policy,
	}
	registry, err := newQuickstartRegistry(cfg, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("newQuickstartRegistry: %v", err)
	}
	if _, _, ok := registry.Get("read_file"); !ok {
		t.Fatal("expected read_file to be registered")
	}
}

func TestNewAgent_ModelFromCuratedList(t *testing.T) {
	t.Parallel()
	models := openai_compatible.CuratedModels()
	if len(models) == 0 {
		t.Skip("no curated models")
	}
	cfg := AgentConfig{
		ProviderID:    "openai-compatible",
		BaseURL:       "http://localhost:1",
		APIKey:        "test-key",
		WorkspaceRoot: t.TempDir(),
		ModelID:       models[0].ID,
	}
	a, err := NewAgent(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewAgent with curated model: %v", err)
	}
	if a == nil {
		t.Fatal("expected agent")
	}
}
