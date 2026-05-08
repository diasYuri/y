//go:build feature_wasm_ext

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const exampleManifest = `id = "y.example.cli-test"
name = "CLI Test"
version = "0.0.1"
api_version = "pi.wasm.v1"
entry = "module.wasm"

[runtime]
memory_pages = 16
timeout_ms = 1000

[[tools]]
name = "say_hi"
description = "Says hi."
`

// writeExampleExtension drops a manifest plus a placeholder wasm file so
// the discovery path succeeds without invoking the wazero runtime.
func writeExampleExtension(t *testing.T, root, id, manifest string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "module.wasm"), []byte("\x00asm\x01\x00\x00\x00"), 0o644); err != nil {
		t.Fatalf("write wasm: %v", err)
	}
	return dir
}

func TestRunExtensionListUsesDirOverride(t *testing.T) {
	root := t.TempDir()
	writeExampleExtension(t, root, "y.example.cli-test", exampleManifest)

	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"extension", "list", "--dir", root}, BuildInfo{Version: "test"})
	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "y.example.cli-test") || !strings.Contains(got, "discovered") {
		t.Fatalf("unexpected list output:\n%s", got)
	}
}

func TestRunExtensionInfo(t *testing.T) {
	root := t.TempDir()
	writeExampleExtension(t, root, "y.example.cli-test", exampleManifest)

	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"extension", "info", "--dir", root, "y.example.cli-test"}, BuildInfo{Version: "test"})
	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{"id: y.example.cli-test", "tools:", "say_hi"} {
		if !strings.Contains(got, want) {
			t.Fatalf("info output missing %q:\n%s", want, got)
		}
	}
}

func TestRunExtensionValidate(t *testing.T) {
	root := t.TempDir()
	writeExampleExtension(t, root, "y.example.cli-test", exampleManifest)

	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"extension", "validate", filepath.Join(root, "y.example.cli-test")}, BuildInfo{Version: "test"})
	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "manifest valid") || !strings.Contains(got, "say_hi") {
		t.Fatalf("validate output unexpected:\n%s", got)
	}
}

func TestRunExtensionValidateRejectsBadManifest(t *testing.T) {
	root := t.TempDir()
	manifest := strings.Replace(exampleManifest, `api_version = "pi.wasm.v1"`, `api_version = "pi.wasm.v0"`, 1)
	writeExampleExtension(t, root, "y.example.cli-test", manifest)

	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"extension", "validate", filepath.Join(root, "y.example.cli-test")}, BuildInfo{Version: "test"})
	if code != exitCodeConfig {
		t.Fatalf("Run returned %d, want %d", code, exitCodeConfig)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "api_version") {
		t.Fatalf("stderr should mention api_version: %q", got)
	}
}

func TestRunExtensionEnableDisable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("Y_CODING_AGENT_DIR", filepath.Join(root, "agent"))

	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"extension", "enable", "y.example.cli-test"}, BuildInfo{Version: "test"})
	if code != 0 {
		t.Fatalf("enable returned %d, want 0; stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "enabled") {
		t.Fatalf("enable output unexpected: %q", got)
	}

	registryPath := filepath.Join(root, "agent", "extensions.toml")
	contents, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if !strings.Contains(string(contents), "y.example.cli-test = true") {
		t.Fatalf("registry should mark extension enabled:\n%s", string(contents))
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(&stdout, &stderr, []string{"extension", "disable", "y.example.cli-test"}, BuildInfo{Version: "test"})
	if code != 0 {
		t.Fatalf("disable returned %d, want 0; stderr=%q", code, stderr.String())
	}
	contents, err = os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if !strings.Contains(string(contents), "y.example.cli-test = false") {
		t.Fatalf("registry should mark extension disabled:\n%s", string(contents))
	}
}

func TestRunExtensionUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{"extension", "blarg"}, BuildInfo{Version: "test"})
	if code != exitCodeUsage {
		t.Fatalf("Run returned %d, want %d", code, exitCodeUsage)
	}
	if got := stderr.String(); !strings.Contains(got, "unknown subcommand") {
		t.Fatalf("stderr should mention unknown subcommand: %q", got)
	}
}
