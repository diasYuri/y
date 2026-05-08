//go:build feature_wasm_ext

package hello_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yuri/y/pkg/extensions/wasm"
	"github.com/yuri/y/pkg/extensions/wasm/wasmtest"
)

const exampleID = "y.examples.hello"

// TestHelloExampleManifest checks that the example's manifest still
// parses cleanly. It guards against the file drifting away from the
// pi.wasm.v1 contract.
func TestHelloExampleManifest(t *testing.T) {
	manifestPath := manifestPath(t)
	manifest, err := wasm.ReadManifest(manifestPath)
	if err != nil {
		t.Fatalf("ReadManifest(%s) returned error: %v", manifestPath, err)
	}
	if manifest.ID != exampleID {
		t.Fatalf("manifest id = %q, want %q", manifest.ID, exampleID)
	}
	if len(manifest.Tools) != 1 || manifest.Tools[0].Name != "hello_say" {
		t.Fatalf("expected exactly one tool named hello_say, got %+v", manifest.Tools)
	}
	if manifest.APIVersion != wasm.SupportedAPIVersion {
		t.Fatalf("manifest api_version = %q, want %q",
			manifest.APIVersion, wasm.SupportedAPIVersion)
	}
}

// TestHelloExampleEndToEnd reuses the manifest plus a synthetic
// ABI-compliant module to exercise the full Manager.CallTool path. The
// synthetic module returns the same payload the TinyGo guest in
// tinygo/main.go would emit for hello_say, so the assertions reflect the
// extension's real contract.
func TestHelloExampleEndToEnd(t *testing.T) {
	manifestPath := manifestPath(t)
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadFile(manifest): %v", err)
	}

	expectedText := "hello, world!"
	wasmBytes := wasmtest.BuildABIToolModule(mustEncodeToolResponse(t, expectedText))

	root := t.TempDir()
	extDir := filepath.Join(root, exampleID)
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extDir, wasm.ManifestFileName), manifestBytes, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "module.wasm"), wasmBytes, 0o644); err != nil {
		t.Fatalf("write wasm: %v", err)
	}

	manager := wasm.NewManager(wasm.Config{
		ExtensionDirs: []string{root},
		Policy:        wasm.AllowAllPolicy(),
		HostVersion:   "test",
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	if err := manager.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	infos := manager.List()
	if len(infos) != 1 || infos[0].Manifest.ID != exampleID {
		t.Fatalf("expected single discovered extension %q, got %+v", exampleID, infos)
	}

	resp, err := manager.CallTool(context.Background(), exampleID, wasm.ToolRequest{
		Tool:      "hello_say",
		Arguments: json.RawMessage(`{"name":"world"}`),
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" || resp.Content[0].Text != expectedText {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func manifestPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	return filepath.Join(wd, "extension.toml")
}

func mustEncodeToolResponse(t *testing.T, text string) []byte {
	t.Helper()
	body, err := json.Marshal(wasm.ToolCallResponse{
		Content: []wasm.ContentBlock{{Type: "text", Text: text}},
	})
	if err != nil {
		t.Fatalf("marshal tool response: %v", err)
	}
	envelope, err := wasm.MarshalResponse(wasm.Response{OK: true, Payload: body})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return envelope
}
