package config

import (
	"strings"
	"testing"
)

type testCapabilities map[CapabilityKind]map[string]bool

func (c testCapabilities) IsKnown(kind CapabilityKind, id string) bool {
	_, ok := c[kind][id]
	return ok
}

func (c testCapabilities) IsCompiled(kind CapabilityKind, id string) bool {
	return c[kind][id]
}

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

func TestValidateCapabilityMatrix(t *testing.T) {
	capabilities := testCapabilities{
		KindFeature: {"git": true},
	}

	if err := Validate(Config{Features: map[string]bool{"git": true}}, capabilities); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	if err := Validate(Config{Features: map[string]bool{"missing": false}}, capabilities); err == nil || !strings.Contains(err.Error(), `unknown feature "missing"`) {
		t.Fatalf("Validate error = %v, want unknown capability error", err)
	}

	if err := Validate(Config{Features: map[string]bool{"git": false}}, testCapabilities{KindFeature: {"git": false}}); err != nil {
		t.Fatalf("disabled uncompiled capability returned error: %v", err)
	}
	if err := Validate(Config{Features: map[string]bool{"git": true}}, testCapabilities{KindFeature: {"git": false}}); err == nil || !strings.Contains(err.Error(), `feature "git" requested by config but not compiled into this binary`) {
		t.Fatalf("Validate error = %v, want uncompiled capability error", err)
	}
}

func TestApplyEnvironmentUsesInjectedLookup(t *testing.T) {
	cfg := ApplyEnvironment(Config{}, func(key string) string {
		if key == "Y_OFFLINE" {
			return "true"
		}
		return ""
	})
	if !cfg.OfflineMode {
		t.Fatal("ApplyEnvironment did not apply injected Y_OFFLINE")
	}
}

func TestGenerateDefault(t *testing.T) {
	got := GenerateDefault()
	for _, section := range []string{"[features]", "[providers]", "[tools]", "[limits]"} {
		if !strings.Contains(got, section) {
			t.Fatalf("default config missing %s", section)
		}
	}
}
