package app

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/diasYuri/y/internal/storage"
	runtimeconfig "github.com/diasYuri/y/pkg/config"
	memoryext "github.com/diasYuri/y/pkg/extensions/memory"
	wasmext "github.com/diasYuri/y/pkg/extensions/wasm"
)

const memoryExtensionID = "memory"

// loadRuntimeConfig loads the generic configuration consumed by extension
// composition. Missing configuration is equivalent to an empty configuration;
// extension defaults are applied by the extension itself.
func loadRuntimeConfig() (runtimeconfig.Config, error) {
	loaded, err := runtimeconfig.LoadFile(storage.DefaultConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return runtimeconfig.Config{}, nil
	}
	if err != nil {
		return runtimeconfig.Config{}, err
	}
	return loaded, nil
}

func splitConfigList(value string) []string {
	var values []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}

func cloneSettings(settings map[string]string) map[string]string {
	clone := make(map[string]string, len(settings))
	for key, value := range settings {
		clone[key] = value
	}
	return clone
}

// validateConfiguredExtensions delegates extension-specific validation without
// constructing stores or starting workers. Unknown extension namespaces are
// intentionally left available for separately installed hosts.
func validateConfiguredExtensions(cfg runtimeconfig.Config) error {
	var settings map[string]string
	if cfg.HasExplicitComposition() {
		for _, entry := range cfg.Components {
			if !entry.Enabled || entry.Type != "extensions.memory" {
				continue
			}
			settings = cloneSettings(entry.Settings)
		}
	} else {
		settings = cloneSettings(cfg.Extensions[memoryExtensionID])
	}
	if len(settings) != 0 {
		if _, err := memoryext.ConfigFromSettings(settings, func(string) string { return "" }); err != nil {
			return err
		}
	}
	wasmSettings := cfg.Extensions["wasm"]
	if len(splitConfigList(wasmSettings["enabled_ids"])) == 0 {
		return nil
	}
	if !wasmext.HostAvailable() {
		return errors.New("WASM extensions are enabled, but this binary was built without feature_wasm_ext")
	}
	if _, err := wasmext.ParseCapabilities(splitConfigList(wasmSettings["allowed_capabilities"])); err != nil {
		return fmt.Errorf("WASM allowed_capabilities: %w", err)
	}
	return nil
}
