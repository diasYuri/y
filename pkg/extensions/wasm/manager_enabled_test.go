//go:build feature_wasm_ext

package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// minimalWASM is a hand-crafted module that satisfies the pi.wasm.v1 ABI.
// It is used by tests that need a real instantiation but do not exercise
// the host-call path.
var minimalWASM = buildABIToolModule(mustMarshalResponse(toolResponseEnvelope("ok")))

// toolResponseEnvelope helps tests build the JSON response the guest is
// expected to return.
func toolResponseEnvelope(text string) Response {
	body, err := json.Marshal(ToolCallResponse{
		Content: []ContentBlock{{Type: "text", Text: text}},
	})
	if err != nil {
		panic(err)
	}
	return Response{OK: true, Payload: body}
}

func mustMarshalResponse(r Response) []byte {
	body, err := MarshalResponse(r)
	if err != nil {
		panic(err)
	}
	return body
}

func TestEnabledManagerHostAvailable(t *testing.T) {
	if !HostAvailable() {
		t.Fatal("HostAvailable() should be true when feature_wasm_ext is set")
	}
}

func TestEnabledManagerLazyInstantiation(t *testing.T) {
	root := t.TempDir()
	extDir := filepath.Join(root, "fake.search")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extDir, ManifestFileName), []byte(validManifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "module.wasm"), minimalWASM, 0o644); err != nil {
		t.Fatalf("write wasm: %v", err)
	}

	m := NewManager(Config{ExtensionDirs: []string{root}, LazyLoad: true})
	defer m.Close(context.Background())

	if err := m.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	info, err := m.Get("fake.search")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.Status != StatusDiscovered {
		t.Fatalf("status before Load = %q, want %q", info.Status, StatusDiscovered)
	}
	if err := m.Load(context.Background(), "fake.search"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	loaded, _ := m.Get("fake.search")
	if loaded.Status != StatusLoaded {
		t.Fatalf("status after Load = %q, want %q", loaded.Status, StatusLoaded)
	}

	// Unload returns the module to discovered state.
	if err := m.Unload(context.Background(), "fake.search"); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	again, _ := m.Get("fake.search")
	if again.Status != StatusDiscovered {
		t.Fatalf("status after Unload = %q, want %q", again.Status, StatusDiscovered)
	}
}

func TestEnabledManagerLoadInvalidWASM(t *testing.T) {
	root := t.TempDir()
	extDir := filepath.Join(root, "broken.module")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := `id = "broken.module"
name = "Broken"
version = "0.0.1"
api_version = "pi.wasm.v1"
entry = "module.wasm"
`
	if err := os.WriteFile(filepath.Join(extDir, ManifestFileName), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "module.wasm"), []byte("not wasm"), 0o644); err != nil {
		t.Fatalf("write wasm: %v", err)
	}

	m := NewManager(Config{ExtensionDirs: []string{root}})
	defer m.Close(context.Background())

	if err := m.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	err := m.Load(context.Background(), "broken.module")
	if err == nil {
		t.Fatal("expected Load to fail with invalid wasm bytes")
	}
	if errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("active host should not return ErrHostUnavailable: %v", err)
	}
	info, _ := m.Get("broken.module")
	if info.Status != StatusFailed {
		t.Fatalf("status after failed Load = %q, want %q", info.Status, StatusFailed)
	}
}

// TestEnabledManagerCallToolReturnsResponse exercises the full ABI path
// from CallTool through to the guest response. The hand-crafted guest
// returns a fixed JSON envelope so we verify both encoding and decoding.
func TestEnabledManagerCallToolReturnsResponse(t *testing.T) {
	expected := toolResponseEnvelope("hello world")
	wasmBytes := buildABIToolModule(mustMarshalResponse(expected))

	root := t.TempDir()
	extDir := filepath.Join(root, "fake.search")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extDir, ManifestFileName), []byte(validManifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "module.wasm"), wasmBytes, 0o644); err != nil {
		t.Fatalf("write wasm: %v", err)
	}

	m := NewManager(Config{ExtensionDirs: []string{root}, Policy: AllowAllPolicy()})
	defer m.Close(context.Background())
	if err := m.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	resp, err := m.CallTool(context.Background(), "fake.search", ToolRequest{
		Tool:      "search",
		Arguments: json.RawMessage(`{"q":"foo"}`),
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "hello world" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

// TestEnabledManagerCallToolTrap verifies that a guest trap surfaces as a
// structured ExtensionError instead of crashing the host.
func TestEnabledManagerCallToolTrap(t *testing.T) {
	root := t.TempDir()
	extDir := filepath.Join(root, "trap.search")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := `id = "trap.search"
name = "Trap"
version = "0.0.1"
api_version = "pi.wasm.v1"
entry = "module.wasm"
`
	if err := os.WriteFile(filepath.Join(extDir, ManifestFileName), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "module.wasm"), buildABITrappingModule(), 0o644); err != nil {
		t.Fatalf("write wasm: %v", err)
	}

	m := NewManager(Config{ExtensionDirs: []string{root}, Policy: AllowAllPolicy()})
	defer m.Close(context.Background())
	if err := m.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	_, err := m.CallTool(context.Background(), "trap.search", ToolRequest{Tool: "explode"})
	if err == nil {
		t.Fatal("expected error from trapping guest")
	}
	if !IsCode(err, CodeTrap) {
		t.Fatalf("expected trap code, got %v", err)
	}
	// The host process should still be healthy; subsequent calls keep
	// returning structured errors instead of panicking.
	_, err = m.CallTool(context.Background(), "trap.search", ToolRequest{Tool: "still_explodes"})
	if err == nil || !IsCode(err, CodeTrap) {
		t.Fatalf("second call should keep trapping, got %v", err)
	}
}
