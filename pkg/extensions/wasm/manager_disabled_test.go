//go:build !feature_wasm_ext

package wasm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDisabledManagerLoadReturnsErrHostUnavailable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "fake.search")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFileName), []byte(validManifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "module.wasm"), []byte("\x00asm\x01\x00\x00\x00"), 0o644); err != nil {
		t.Fatalf("write wasm: %v", err)
	}

	m := NewManager(Config{ExtensionDirs: []string{root}})
	if err := m.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if HostAvailable() {
		t.Fatal("HostAvailable() should be false when feature_wasm_ext is not set")
	}
	err := m.Load(context.Background(), "fake.search")
	if !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("expected ErrHostUnavailable, got %v", err)
	}
}
