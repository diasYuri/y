//go:build feature_wasm_ext

package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/diasYuri/y/pkg/component"
	runtimeconfig "github.com/diasYuri/y/pkg/config"
	"github.com/diasYuri/y/pkg/extensions/wasm/wasmtest"
)

func TestComposition_WASMPreflightAndActivation(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "activate"
		if missing {
			name = "missing dependency"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "guest")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			manifest := `id = "guest"
name = "Guest"
version = "1.0.0"
api_version = "y.wasm.v1"
entry = "module.wasm"
[[tools]]
name = "echo"
`
			if missing {
				manifest += `[[requires]]
service_id = "missing.service"
provider_id = "extensions/wasm.missing"
version = "1.0.0"
`
			}
			if err := os.WriteFile(filepath.Join(dir, "extension.toml"), []byte(manifest), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "module.wasm"), wasmtest.BuildABIToolModule([]byte(`{"ok":true}`)), 0o644); err != nil {
				t.Fatal(err)
			}
			config := testCompositionConfig()
			config.Components["extensions"] = runtimeconfig.ComponentConfig{Type: "extensions.host", Enabled: true}
			config.Bindings["extensions_host"] = "extensions"
			config.Extensions = map[string]map[string]string{"wasm": {"enabled_ids": "guest", "directories": root}}
			if missing {
				err := validateWASMSelection(context.Background(), config, CompositionOptions{})
				if !errors.Is(err, component.ErrMissingService) {
					t.Fatalf("preflight=%v", err)
				}
				return
			}
			compiled := testCompositionCapabilities(t)
			if err := compiled.AddComponent("extensions.host", "", "test"); err != nil {
				t.Fatal(err)
			}
			composition, err := BuildComposition(context.Background(), config, compiled, CompositionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if got := composition.Registry.List(); len(got) != 1 || got[0].Name != "wasm_guest_echo" {
				t.Fatalf("tools=%+v", got)
			}
			if err := composition.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
