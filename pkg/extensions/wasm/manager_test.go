package wasm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validManifest = `id = "fake.search"
name = "Fake"
version = "0.0.1"
api_version = "pi.wasm.v1"
entry = "module.wasm"

[runtime]
memory_pages = 16
timeout_ms = 1000
`

func writeExtension(t *testing.T, root, id, manifest string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifestPath := filepath.Join(dir, ManifestFileName)
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	wasmPath := filepath.Join(dir, "module.wasm")
	if err := os.WriteFile(wasmPath, []byte("\x00asm\x01\x00\x00\x00"), 0o644); err != nil {
		t.Fatalf("write wasm: %v", err)
	}
	return dir
}

func TestManagerDiscoversExtensions(t *testing.T) {
	root := t.TempDir()
	writeExtension(t, root, "fake.search", validManifest)
	writeExtension(t, root, "fake.indexer", strings.Replace(validManifest, "fake.search", "fake.indexer", 1))

	m := NewManager(Config{ExtensionDirs: []string{root}})
	if err := m.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	infos := m.List()
	if len(infos) != 2 {
		t.Fatalf("expected 2 extensions, got %d", len(infos))
	}
	if infos[0].Manifest.ID != "fake.indexer" || infos[1].Manifest.ID != "fake.search" {
		t.Fatalf("List returned unsorted ids: %+v", infos)
	}
	for _, info := range infos {
		if info.Status != StatusDiscovered {
			t.Errorf("expected StatusDiscovered, got %q", info.Status)
		}
	}
}

func TestManagerDiscoverIgnoresMissingDir(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "does", "not", "exist")
	m := NewManager(Config{ExtensionDirs: []string{missing}})
	if err := m.Discover(context.Background()); err != nil {
		t.Fatalf("Discover with missing dir should succeed, got %v", err)
	}
	if got := len(m.List()); got != 0 {
		t.Fatalf("expected zero extensions, got %d", got)
	}
}

func TestManagerDiscoverInvalidManifest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "broken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bad := `id = "broken"
name = "broken"
version = "0.0.1"
api_version = "pi.wasm.v0"
entry = "module.wasm"
`
	if err := os.WriteFile(filepath.Join(dir, ManifestFileName), []byte(bad), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := NewManager(Config{ExtensionDirs: []string{root}})
	err := m.Discover(context.Background())
	if err == nil {
		t.Fatal("expected discover error for invalid manifest")
	}
	var me *ManifestError
	if !errors.As(err, &me) {
		t.Fatalf("expected ManifestError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "api_version") {
		t.Errorf("error %q should reference api_version", err)
	}
	// Even though the manifest failed, no extension should be registered.
	if got := len(m.List()); got != 0 {
		t.Fatalf("expected no successful extensions, got %d", got)
	}
}

func TestManagerLoadUnknownExtension(t *testing.T) {
	m := NewManager(Config{})
	err := m.Load(context.Background(), "missing.extension")
	if err == nil {
		t.Fatal("expected error loading unknown extension")
	}
	if !errors.Is(err, ErrExtensionNotFound) {
		t.Fatalf("expected ErrExtensionNotFound, got %v", err)
	}
}

func TestManagerLoadIsLazy(t *testing.T) {
	root := t.TempDir()
	writeExtension(t, root, "fake.search", validManifest)

	m := NewManager(Config{ExtensionDirs: []string{root}, LazyLoad: true})
	if err := m.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	info, err := m.Get("fake.search")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.Status != StatusDiscovered {
		t.Fatalf("expected StatusDiscovered before Load, got %q", info.Status)
	}
}

func TestManagerCloseIsIdempotent(t *testing.T) {
	m := NewManager(Config{})
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
