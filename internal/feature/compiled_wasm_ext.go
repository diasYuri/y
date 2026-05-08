//go:build feature_wasm_ext

package feature

func registerWASMExtensionsIfCompiled(r *Registry) error {
	if err := r.AddFeature("wasm_extensions", "feature_wasm_ext", "WASM extension host."); err != nil {
		return err
	}
	for _, cmd := range []struct{ id, desc string }{
		{"extension.disable", "Disable a WASM extension in runtime config."},
		{"extension.enable", "Enable a WASM extension in runtime config."},
		{"extension.info", "Print metadata about a discovered WASM extension."},
		{"extension.list", "List discovered WASM extensions."},
		{"extension.validate", "Validate a WASM extension manifest."},
	} {
		if err := r.AddCommand(cmd.id, cmd.desc); err != nil {
			return err
		}
	}
	return nil
}
