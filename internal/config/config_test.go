package config

import (
	"strings"
	"testing"

	"github.com/yuri/y/internal/feature"
)

func TestParseDeclarativeConfig(t *testing.T) {
	cfg, err := Parse(strings.NewReader(`
[features]
git = true

[providers]
openai = true

[tools]
git_status = true
run_command = false

[limits]
max_file_read_bytes = 1048576
command_timeout_seconds = 30
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if !cfg.Features["git"] {
		t.Fatalf("features.git = false, want true")
	}
	if !cfg.Providers["openai"] {
		t.Fatalf("providers.openai = false, want true")
	}
	if !cfg.Tools["git_status"] {
		t.Fatalf("tools.git_status = false, want true")
	}
	if got := cfg.Limits["command_timeout_seconds"]; got != 30 {
		t.Fatalf("command_timeout_seconds = %d, want 30", got)
	}
}

func TestValidateCompiledConfigMatrix(t *testing.T) {
	tests := []struct {
		name       string
		compiled   bool
		enabled    bool
		wantErrSub string
	}{
		{name: "compiled enabled", compiled: true, enabled: true},
		{name: "compiled disabled", compiled: true, enabled: false},
		{name: "uncompiled disabled", compiled: false, enabled: false},
		{
			name:       "uncompiled enabled",
			compiled:   false,
			enabled:    true,
			wantErrSub: `feature "git" requested by config but not compiled into this binary`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := feature.NewRegistry()
			if tt.compiled {
				if err := reg.AddFeature("git", "feature_git", "Git integration."); err != nil {
					t.Fatalf("AddFeature returned error: %v", err)
				}
			}

			err := Validate(Config{Features: map[string]bool{"git": tt.enabled}}, reg)
			if tt.wantErrSub == "" {
				if err != nil {
					t.Fatalf("Validate returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("Validate error = %v, want substring %q", err, tt.wantErrSub)
			}
		})
	}
}

func TestValidateProviderAndToolNeedCompiledCapability(t *testing.T) {
	cfg := Config{
		Providers: map[string]bool{"openai": true},
		Tools:     map[string]bool{"run_command": true},
	}

	err := Validate(cfg, feature.NewRegistry())
	if err == nil || !strings.Contains(err.Error(), `provider "openai" requested by config but not compiled into this binary`) {
		t.Fatalf("Validate error = %v, want uncompiled provider error", err)
	}

	reg := feature.NewRegistry()
	if err := reg.AddProvider("openai", "feature_openai", "OpenAI provider."); err != nil {
		t.Fatalf("AddProvider returned error: %v", err)
	}

	err = Validate(cfg, reg)
	if err == nil || !strings.Contains(err.Error(), `tool "run_command" requested by config but not compiled into this binary`) {
		t.Fatalf("Validate error = %v, want uncompiled tool error", err)
	}
}

func TestValidateRejectsUnknownConfigKey(t *testing.T) {
	err := Validate(Config{Features: map[string]bool{"does_not_exist": true}}, feature.NewRegistry())
	if err == nil || !strings.Contains(err.Error(), `unknown feature "does_not_exist"`) {
		t.Fatalf("Validate error = %v, want unknown feature error", err)
	}
}

func TestGenerateDefault(t *testing.T) {
	got := GenerateDefault()
	if !strings.Contains(got, "[features]") {
		t.Fatal("missing [features] section")
	}
	if !strings.Contains(got, "[providers]") {
		t.Fatal("missing [providers] section")
	}
	if !strings.Contains(got, "[tools]") {
		t.Fatal("missing [tools] section")
	}
	if !strings.Contains(got, "[limits]") {
		t.Fatal("missing [limits] section")
	}
}
