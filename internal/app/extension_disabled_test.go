//go:build !feature_wasm_ext

package app

import (
	"bytes"
	"strings"
	"testing"
)

// TestExtensionCommandsAbsentWithoutBuildTag asserts that builds without
// feature_wasm_ext refuse the extension subcommands rather than silently
// no-op'ing. This is the contract from extension-wasm.md §5.
func TestExtensionCommandsAbsentWithoutBuildTag(t *testing.T) {
	for _, sub := range []string{"list", "info", "validate", "enable", "disable"} {
		t.Run(sub, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(&stdout, &stderr, []string{"extension", sub}, BuildInfo{Version: "test"})
			if code != exitCodeUsage {
				t.Fatalf("Run returned %d, want %d; stderr=%q", code, exitCodeUsage, stderr.String())
			}
			if got := stderr.String(); !strings.Contains(got, "feature_wasm_ext") {
				t.Fatalf("stderr should reference feature_wasm_ext: %q", got)
			}
		})
	}
}
