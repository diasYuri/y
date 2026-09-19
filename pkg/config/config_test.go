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
	if strings.Contains(got, "[memory]") || strings.Contains(got, "[extensions.memory]") {
		t.Fatal("default config must not select a built-in extension")
	}
}

func TestParseExtensionConfig(t *testing.T) {
	cfg, err := Parse(strings.NewReader(`[extensions.memory]
enabled = true
mode = "automatic"
profile = "stateless"
backend = "noop"
directory = "/tmp/y-memory"
max_items = 7
max_context_tokens = 900
max_read_bytes = 1234
auto_extract = false
dedicated_tools = true
fail_open = false
`))
	if err != nil {
		t.Fatal(err)
	}
	memory := cfg.Extensions["memory"]
	if memory["enabled"] != "true" || memory["mode"] != "automatic" || memory["profile"] != "stateless" || memory["backend"] != "noop" {
		t.Fatalf("memory config = %#v", memory)
	}
	if memory["max_items"] != "7" || memory["max_context_tokens"] != "900" || memory["max_read_bytes"] != "1234" || memory["auto_extract"] != "false" || memory["dedicated_tools"] != "true" || memory["fail_open"] != "false" {
		t.Fatalf("memory limits/options = %#v", memory)
	}
	if err := Validate(cfg, testCapabilities{}); err != nil {
		t.Fatalf("Validate extension config: %v", err)
	}
}

func TestParseRejectsLegacyMemorySection(t *testing.T) {
	_, err := Parse(strings.NewReader("[memory]\nenabled = true\n"))
	if err == nil || !strings.Contains(err.Error(), `unsupported section "memory"`) {
		t.Fatalf("Parse error = %v, want legacy memory section rejection", err)
	}
}

func TestApplyEnvironmentDoesNotKnowExtensions(t *testing.T) {
	cfg := Config{Extensions: map[string]map[string]string{"memory": {"enabled": "true"}}}
	got := ApplyEnvironment(cfg, func(key string) string {
		if key == "Y_MEMORY" {
			return "false"
		}
		return ""
	})
	if got.Extensions["memory"]["enabled"] != "true" {
		t.Fatal("core config must not interpret extension environment variables")
	}
}
