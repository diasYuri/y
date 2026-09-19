package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	internalmemory "github.com/diasYuri/y/internal/memory"
	"github.com/diasYuri/y/internal/storage"
	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/ai"
	runtimeconfig "github.com/diasYuri/y/pkg/config"
	runtimeextensions "github.com/diasYuri/y/pkg/extensions"
	memoryext "github.com/diasYuri/y/pkg/extensions/memory"
	"github.com/diasYuri/y/pkg/tools"
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

// buildExtensionHost is the application composition root. It is the only
// place that knows which built-in extensions ship with this binary and which
// private adapters satisfy their public contracts.
func buildExtensionHost(
	ctx context.Context,
	cfg runtimeconfig.Config,
	opts headlessOptions,
	provider agent.Provider,
	cwd string,
	registry *tools.Registry,
) (*runtimeextensions.Host, error) {
	host := runtimeextensions.NewHost(registry)
	settings := cloneSettings(cfg.Extensions[memoryExtensionID])
	for key, value := range opts.extensionSettings[memoryExtensionID] {
		settings[key] = value
	}
	config, err := memoryext.ConfigFromSettings(settings, os.Getenv)
	if err != nil {
		return nil, err
	}
	if opts.disabledExtensions[memoryExtensionID] {
		config.Enabled = false
		config.AutoExtract = false
	}
	if config.WorkspaceID == "" {
		config.WorkspaceID = cwd
	}
	if config.ProjectID == "" {
		config.ProjectID = cwd
	}
	// The extension owns the memory identity, but the generic runtime must use
	// the same identity when resolving context. Otherwise memories are written
	// under the configured namespace and searched under the process cwd.
	host.AddAgentOption(agent.WithContextIdentity(config.TenantID, config.WorkspaceID, config.ProjectID, "", ""))

	extOptions := []memoryext.Option{
		memoryext.WithConfig(config),
		memoryext.WithExtractor(internalmemory.NewProviderExtractor(provider, ai.Model{ID: opts.model})),
	}
	if config.Enabled && config.Profile != "stateless" && config.Profile != "ephemeral" && config.Backend == "filesystem" {
		directory := config.Directory
		if directory == "" {
			if root := os.Getenv("Y_CODING_AGENT_DIR"); root != "" {
				directory = filepath.Join(root, "memory")
			} else {
				directory = filepath.Join(storage.DefaultAgentDir(), "memory")
			}
		}
		store, storeErr := internalmemory.NewFilesystemStore(directory)
		if storeErr != nil {
			return nil, storeErr
		}
		extOptions = append(extOptions, memoryext.WithStore(store))
	} else if config.Profile == "stateless" || config.Profile == "ephemeral" || config.Backend == "noop" || config.Backend == "memory" {
		extOptions = append(extOptions, memoryext.WithStore(memoryext.NoopStore{}))
	}

	extension, err := memoryext.New(extOptions...)
	if err != nil {
		return nil, err
	}
	if err := host.Install(extension); err != nil {
		_ = extension.Close(ctx)
		return nil, fmt.Errorf("install extension %q: %w", extension.ID(), err)
	}
	return host, nil
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
	settings, configured := cfg.Extensions[memoryExtensionID]
	if !configured {
		return nil
	}
	_, err := memoryext.ConfigFromSettings(settings, func(string) string { return "" })
	return err
}
