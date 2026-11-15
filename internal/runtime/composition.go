package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/diasYuri/y/internal/buildinfo"
	internalconfig "github.com/diasYuri/y/internal/config"
	"github.com/diasYuri/y/internal/feature"
	"github.com/diasYuri/y/internal/memory"
	"github.com/diasYuri/y/internal/storage"
	internaltelemetry "github.com/diasYuri/y/internal/telemetry"
	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/component"
	runtimeconfig "github.com/diasYuri/y/pkg/config"
	"github.com/diasYuri/y/pkg/extensions"
	memoryext "github.com/diasYuri/y/pkg/extensions/memory"
	"github.com/diasYuri/y/pkg/extensions/subagents"
	taskmanager "github.com/diasYuri/y/pkg/extensions/task-manager"
	wasmext "github.com/diasYuri/y/pkg/extensions/wasm"
	"github.com/diasYuri/y/pkg/policy"
	publicruntime "github.com/diasYuri/y/pkg/runtime"
	"github.com/diasYuri/y/pkg/session"
	"github.com/diasYuri/y/pkg/telemetry"
	"github.com/diasYuri/y/pkg/tools"
)

const builtinComponentVersion = "1.0.0"

// ProviderFactory creates the configured provider when its component starts.
// The selected ID is empty for provider.auto and concrete for provider.<id>.
type ProviderFactory func(context.Context, string) (agent.Provider, error)

// CompositionOptions are process-local inputs captured by built-in component
// factories. They are not retained in public config or serialized snapshots.
type CompositionOptions struct {
	CWD                 string
	Model               string
	SessionDir          string
	RequireSessionStore bool
	ProviderFactory     ProviderFactory
	ApprovalHandler     tools.ApprovalHandler
	DisabledExtensions  map[string]bool
	ExtensionSettings   map[string]map[string]string
}

// Composition is the running binary composition shared by CLI and RPC.
type Composition struct {
	Harness      *publicruntime.Harness
	Provider     agent.Provider
	Registry     *tools.Registry
	Host         *extensions.Host
	SessionStore session.Store
	Telemetry    telemetry.Emitter
}

// Close releases every component in reverse dependency order.
func (c *Composition) Close(ctx context.Context) error {
	if c == nil || c.Harness == nil {
		return nil
	}
	return c.Harness.Close(ctx)
}

// BuildComposition validates config and the dependency graph before starting
// any component factory that can acquire a resource or perform I/O.
func BuildComposition(ctx context.Context, cfg runtimeconfig.Config, compiled *feature.Registry, options CompositionOptions) (*Composition, error) {
	if compiled == nil {
		return nil, errors.New("runtime composition: compiled capability registry is nil")
	}
	if options.ProviderFactory == nil {
		return nil, errors.New("runtime composition: provider factory is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, definitions, err := prepareComposition(ctx, cfg, compiled, options)
	if err != nil {
		return nil, err
	}
	harness, err := publicruntime.NewHarness(definitions...)
	if err != nil {
		return nil, fmt.Errorf("validate runtime composition: %w", err)
	}
	if err := harness.Start(ctx); err != nil {
		return nil, fmt.Errorf("start runtime composition: %w", err)
	}

	composition := &Composition{Harness: harness}
	composition.Provider, err = publicruntime.Resolve(harness, publicruntime.ProviderKey)
	if err == nil {
		composition.Registry, err = publicruntime.Resolve(harness, publicruntime.ToolRegistryKey)
	}
	if err == nil {
		composition.Telemetry, err = publicruntime.Resolve(harness, publicruntime.TelemetryKey)
	}
	if err == nil && hasEnabledType(normalized, "extensions.host") {
		composition.Host, err = publicruntime.Resolve(harness, publicruntime.ExtensionHostKey)
	}
	if err == nil && hasEnabledType(normalized, "sessions.filesystem") {
		composition.SessionStore, err = publicruntime.Resolve(harness, publicruntime.SessionStoreKey)
	}
	if err != nil {
		_ = harness.Close(context.Background())
		return nil, fmt.Errorf("resolve runtime composition: %w", err)
	}
	return composition, nil
}

// ValidateComposition checks the built-in component catalog, exact component
// versions, settings, bindings, and dependency graph without starting factories.
func ValidateComposition(cfg runtimeconfig.Config, compiled *feature.Registry, options CompositionOptions) error {
	if compiled == nil {
		return errors.New("runtime composition: compiled capability registry is nil")
	}
	_, definitions, err := prepareComposition(context.Background(), cfg, compiled, options)
	if err != nil {
		return err
	}
	_, err = publicruntime.NewHarness(definitions...)
	if err != nil {
		return fmt.Errorf("validate runtime composition: %w", err)
	}
	return nil
}

func prepareComposition(ctx context.Context, cfg runtimeconfig.Config, compiled *feature.Registry, options CompositionOptions) (runtimeconfig.Config, []component.Definition, error) {
	if !cfg.HasExplicitComposition() {
		cfg = legacyComposition(cfg)
	}
	applyRuntimeOverrides(&cfg, options)
	if err := validateCompositionConfig(cfg, compiled, options); err != nil {
		return runtimeconfig.Config{}, nil, err
	}
	if err := validateWASMSelection(ctx, cfg, options); err != nil {
		return runtimeconfig.Config{}, nil, err
	}
	definitions, err := compositionDefinitions(cfg, compiled, options)
	if err != nil {
		return runtimeconfig.Config{}, nil, err
	}
	return cfg, definitions, nil
}

func validateCompositionConfig(cfg runtimeconfig.Config, compiled *feature.Registry, options CompositionOptions) error {
	if err := validateConfig(cfg, compiled); err != nil {
		return err
	}
	if err := validateMemorySettings(cfg, options); err != nil {
		return err
	}
	wasmSettings, err := configuredWASMSettings(cfg, options)
	if err != nil {
		return err
	}
	if len(splitConfigList(wasmSettings["enabled_ids"])) > 0 && !hasEnabledType(cfg, "extensions.host") {
		return errors.New("runtime composition: WASM extensions require an enabled extensions.host component")
	}
	if !hasEnabledProvider(cfg) {
		return errors.New("runtime composition: an enabled provider component is required")
	}
	if !hasEnabledType(cfg, "tools.default") {
		return errors.New("runtime composition: an enabled tools.default component is required")
	}
	if options.RequireSessionStore && !hasEnabledType(cfg, "sessions.filesystem") {
		return errors.New("runtime composition: sessions.filesystem is required when session persistence is enabled")
	}
	if err := validateComponentSettings(cfg); err != nil {
		return err
	}
	if cfg.HasExplicitComposition() {
		for _, serviceID := range []string{"agent_provider", "policy_engine", "telemetry_emitter", "tools_registry"} {
			if cfg.Bindings[serviceID] == "" {
				return fmt.Errorf("runtime composition: binding %q is required", serviceID)
			}
		}
		if hasEnabledType(cfg, "extensions.host") && cfg.Bindings["extensions_host"] == "" {
			return errors.New("runtime composition: binding \"extensions_host\" is required")
		}
		if hasEnabledType(cfg, "sessions.filesystem") && cfg.Bindings["sessions_store"] == "" {
			return errors.New("runtime composition: binding \"sessions_store\" is required")
		}
		for _, binding := range []struct{ serviceID, expectedType string }{
			{"agent_provider", "provider."},
			{"policy_engine", "policy.default"},
			{"telemetry_emitter", "telemetry.default"},
			{"tools_registry", "tools.default"},
			{"extensions_host", "extensions.host"},
			{"sessions_store", "sessions.filesystem"},
		} {
			serviceID, expectedType := binding.serviceID, binding.expectedType
			providerID := cfg.Bindings[serviceID]
			if providerID == "" {
				continue
			}
			entry := cfg.Components[providerID]
			if expectedType == "provider." {
				if !strings.HasPrefix(entry.Type, expectedType) {
					return fmt.Errorf("runtime composition: binding %q must select a provider component, got %q", serviceID, entry.Type)
				}
			} else if entry.Type != expectedType {
				return fmt.Errorf("runtime composition: binding %q must select %q, got %q", serviceID, expectedType, entry.Type)
			}
		}
	}
	return nil
}

func validateConfig(cfg runtimeconfig.Config, compiled *feature.Registry) error {
	return internalconfig.Validate(cfg, compiled)
}

func validateMemorySettings(cfg runtimeconfig.Config, options CompositionOptions) error {
	for _, instanceID := range sortedComponentIDs(cfg.Components) {
		entry := cfg.Components[instanceID]
		if !entry.Enabled || entry.Type != "extensions.memory" {
			continue
		}
		settings := cloneStringMap(entry.Settings)
		if !cfg.HasExplicitComposition() {
			for key, value := range cfg.Extensions["memory"] {
				settings[key] = value
			}
		}
		for key, value := range options.ExtensionSettings["memory"] {
			settings[key] = value
		}
		if options.DisabledExtensions["memory"] {
			settings["enabled"] = "false"
		}
		if _, err := memoryext.ConfigFromSettings(settings, func(string) string { return "" }); err != nil {
			return err
		}
	}
	return nil
}

func validateComponentSettings(cfg runtimeconfig.Config) error {
	for instanceID, entry := range cfg.Components {
		if !entry.Enabled {
			continue
		}
		switch entry.Type {
		case "extensions.memory":
		case "sessions.filesystem":
			for key := range entry.Settings {
				if key != "directory" {
					return fmt.Errorf("runtime composition: sessions.filesystem has unknown setting %q", key)
				}
			}
		case "task_manager":
			for key, value := range entry.Settings {
				if key != "max_tasks" {
					return fmt.Errorf("runtime composition: task_manager has unknown setting %q", key)
				}
				maxTasks, err := strconv.Atoi(value)
				if err != nil || maxTasks <= 0 {
					return fmt.Errorf("runtime composition: task_manager setting max_tasks must be a positive integer")
				}
			}
		default:
			if len(entry.Settings) > 0 {
				return fmt.Errorf("runtime composition: component %q does not accept settings", instanceID)
			}
		}
	}
	return nil
}

func compositionDefinitions(cfg runtimeconfig.Config, compiled *feature.Registry, options CompositionOptions) ([]component.Definition, error) {
	definitions := make([]component.Definition, 0, len(cfg.Components))
	for _, instanceID := range sortedComponentIDs(cfg.Components) {
		entry := cfg.Components[instanceID]
		if !entry.Enabled {
			continue
		}
		if entry.Version != "" && entry.Version != builtinComponentVersion {
			return nil, fmt.Errorf("runtime composition: component %q requires version %q, binary provides %q", entry.Type, entry.Version, builtinComponentVersion)
		}
		entry.Version = builtinComponentVersion
		revision, err := componentRevision(entry)
		if err != nil {
			return nil, fmt.Errorf("runtime composition: fingerprint component %q: %w", instanceID, err)
		}
		var definition component.Definition
		switch {
		case strings.HasPrefix(entry.Type, "provider."):
			selected := strings.TrimPrefix(entry.Type, "provider.")
			if selected == "auto" {
				selected = ""
			}
			factory := options.ProviderFactory
			definition = providerDefinition(instanceID, revision, func(ctx context.Context) (agent.Provider, error) {
				return factory(ctx, selected)
			})
		case entry.Type == "policy.default":
			definition = policyDefinition(instanceID, revision)
		case entry.Type == "telemetry.default":
			definition = telemetryDefinition(instanceID, revision)
		case entry.Type == "tools.default":
			policyProvider, err := binding(cfg, "policy_engine")
			if err != nil {
				return nil, err
			}
			telemetryProvider, err := binding(cfg, "telemetry_emitter")
			if err != nil {
				return nil, err
			}
			definition = toolRegistryDefinition(instanceID, revision, policyProvider, telemetryProvider, compiled, options)
		case entry.Type == "extensions.host":
			registryProvider, err := binding(cfg, "tools_registry")
			if err != nil {
				return nil, err
			}
			wasmSettings, err := configuredWASMSettings(cfg, options)
			if err != nil {
				return nil, err
			}
			policyProvider, err := binding(cfg, "policy_engine")
			if err != nil {
				return nil, err
			}
			definition = extensionHostDefinition(instanceID, revision, registryProvider, policyProvider, wasmSettings)
		case entry.Type == "extensions.memory":
			hostProvider, err := binding(cfg, "extensions_host")
			if err != nil {
				return nil, err
			}
			providerID, err := binding(cfg, "agent_provider")
			if err != nil {
				return nil, err
			}
			settings := cloneStringMap(entry.Settings)
			if !cfg.HasExplicitComposition() {
				for key, value := range cfg.Extensions["memory"] {
					settings[key] = value
				}
			}
			for key, value := range options.ExtensionSettings["memory"] {
				settings[key] = value
			}
			if options.DisabledExtensions["memory"] {
				settings["enabled"] = "false"
			}
			definition = memoryDefinition(instanceID, revision, hostProvider, providerID, settings, options)
		case entry.Type == "sessions.filesystem":
			directory := options.SessionDir
			if directory == "" {
				directory = storage.DefaultAgentDir()
			}
			if configured := entry.Settings["directory"]; configured != "" && options.SessionDir == "" {
				directory = configured
			}
			definition = sessionStoreDefinition(instanceID, revision, directory)
		case entry.Type == "subagents":
			providerID, err := binding(cfg, "agent_provider")
			if err != nil {
				return nil, err
			}
			registryProvider, err := binding(cfg, "tools_registry")
			if err != nil {
				return nil, err
			}
			hostProvider, err := binding(cfg, "extensions_host")
			if err != nil {
				return nil, err
			}
			definition = subagentsDefinition(instanceID, revision, providerID, registryProvider, hostProvider)
		case entry.Type == "task_manager":
			registryProvider, err := binding(cfg, "tools_registry")
			if err != nil {
				return nil, err
			}
			hostProvider, err := binding(cfg, "extensions_host")
			if err != nil {
				return nil, err
			}
			definition = taskManagerDefinition(instanceID, revision, registryProvider, hostProvider, entry.Settings)
		default:
			return nil, fmt.Errorf("runtime composition: component type %q has no built-in adapter", entry.Type)
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func providerDefinition(id, revision string, factory func(context.Context) (agent.Provider, error)) component.Definition {
	descriptor := component.Descriptor{ID: id, Version: builtinComponentVersion, Revision: revision, Provides: []component.ServiceRef{component.Reference(publicruntime.ProviderKey)}}
	return component.Define(descriptor, func() (component.Component, error) {
		return component.ComponentFunc(func(ctx context.Context, scope *component.Scope) error {
			provider, err := factory(ctx)
			if err != nil {
				return err
			}
			if provider == nil {
				return errors.New("provider factory returned nil provider")
			}
			if closer, ok := provider.(interface{ Close() error }); ok {
				if err := scope.Track(func(context.Context) error { return closer.Close() }); err != nil {
					_ = closer.Close()
					return err
				}
			}
			return component.Provide(scope, publicruntime.ProviderKey, provider)
		}), nil
	})
}

func policyDefinition(id, revision string) component.Definition {
	descriptor := component.Descriptor{ID: id, Version: builtinComponentVersion, Revision: revision, Provides: []component.ServiceRef{component.Reference(publicruntime.PolicyKey)}}
	return component.Define(descriptor, func() (component.Component, error) {
		return component.ComponentFunc(func(_ context.Context, scope *component.Scope) error {
			return component.Provide(scope, publicruntime.PolicyKey, policy.NewEngine(policy.DefaultConfig()))
		}), nil
	})
}

func telemetryDefinition(id, revision string) component.Definition {
	descriptor := component.Descriptor{ID: id, Version: builtinComponentVersion, Revision: revision, Provides: []component.ServiceRef{component.Reference(publicruntime.TelemetryKey)}}
	return component.Define(descriptor, func() (component.Component, error) {
		return component.ComponentFunc(func(_ context.Context, scope *component.Scope) error {
			return component.Provide(scope, publicruntime.TelemetryKey, internaltelemetry.DefaultEmitter())
		}), nil
	})
}

func toolRegistryDefinition(id, revision, policyProvider, telemetryProvider string, compiled *feature.Registry, options CompositionOptions) component.Definition {
	descriptor := component.Descriptor{
		ID: id, Version: builtinComponentVersion, Revision: revision,
		Provides: []component.ServiceRef{component.Reference(publicruntime.ToolRegistryKey)},
		Requirements: []component.ServiceRequirement{
			serviceRequirement(publicruntime.PolicyKey, policyProvider),
			serviceRequirement(publicruntime.TelemetryKey, telemetryProvider),
		},
	}
	return component.Define(descriptor, func() (component.Component, error) {
		return component.ComponentFunc(func(ctx context.Context, scope *component.Scope) error {
			policyEngine, err := component.Require(scope, publicruntime.PolicyKey)
			if err != nil {
				return err
			}
			emitter, err := component.Require(scope, publicruntime.TelemetryKey)
			if err != nil {
				return err
			}
			registry, err := BuildToolRegistry(ctx, compiled, options.CWD, policyEngine, options.ApprovalHandler, emitter)
			if err != nil {
				return err
			}
			if err := scope.Track(registry.Close); err != nil {
				_ = registry.Close(context.Background())
				return err
			}
			return component.Provide(scope, publicruntime.ToolRegistryKey, registry)
		}), nil
	})
}

func extensionHostDefinition(id, revision, registryProvider, policyProvider string, wasmSettings map[string]string) component.Definition {
	descriptor := component.Descriptor{
		ID: id, Version: builtinComponentVersion, Revision: revision,
		Provides:     []component.ServiceRef{component.Reference(publicruntime.ExtensionHostKey)},
		Requirements: []component.ServiceRequirement{serviceRequirement(publicruntime.ToolRegistryKey, registryProvider), serviceRequirement(publicruntime.PolicyKey, policyProvider)},
	}
	return component.Define(descriptor, func() (component.Component, error) {
		return component.ComponentFunc(func(ctx context.Context, scope *component.Scope) error {
			registry, err := component.Require(scope, publicruntime.ToolRegistryKey)
			if err != nil {
				return err
			}
			host := extensions.NewHost(registry)
			if err := scope.Track(host.Close); err != nil {
				_ = host.Close(context.Background())
				return err
			}
			engine, err := component.Require(scope, publicruntime.PolicyKey)
			if err != nil {
				return err
			}
			if err := installWASMComponents(ctx, host, wasmSettings, wasmRuntimePolicy(engine)); err != nil {
				return err
			}
			return component.Provide(scope, publicruntime.ExtensionHostKey, host)
		}), nil
	})
}

func installWASMComponents(ctx context.Context, host *extensions.Host, settings map[string]string, policy wasmext.Policy) error {
	ids := splitConfigList(settings["enabled_ids"])
	if len(ids) == 0 {
		return nil
	}
	capabilities, err := wasmext.ParseCapabilities(splitConfigList(settings["allowed_capabilities"]))
	if err != nil {
		return fmt.Errorf("WASM allowed_capabilities: %w", err)
	}
	manager := wasmext.NewManager(wasmext.Config{
		HostVersion:   buildinfo.Current().Version,
		ExtensionDirs: splitConfigList(settings["directories"]), AllowedCapabilities: capabilities,
		Policy: policy,
	})
	if err := host.AddCleanup(manager.Close); err != nil {
		_ = manager.Close(context.Background())
		return err
	}
	if err := manager.Discover(ctx); err != nil {
		return fmt.Errorf("discover WASM extensions: %w", err)
	}
	definitions, err := wasmext.ComponentDefinitions(manager, host, ids...)
	if err != nil {
		return err
	}
	if err := host.AddDefinitions(ctx, definitions); err != nil {
		return fmt.Errorf("activate WASM components: %w", err)
	}
	return nil
}

// wasmRuntimePolicy mediates guest authority through the harness policy. Guest
// approval requests fail closed; no host-call approval continuation is implied.
func wasmRuntimePolicy(engine policy.Engine) wasmext.Policy {
	return wasmext.PolicyFunc(func(ctx context.Context, req wasmext.CapabilityRequest) (wasmext.Decision, error) {
		decision, err := engine.Decide(ctx, policy.Request{
			ToolName:   "wasm." + req.ExtensionID + "." + string(req.Kind),
			Capability: string(req.Capability), Path: req.Detail,
			RequiredCapabilities: []string{string(req.Capability)},
			Identity:             policy.Identity{CallerID: "wasm." + req.ExtensionID},
			Sensitive:            req.Capability != wasmext.CapLogs,
		})
		if err != nil {
			return wasmext.DecisionDeny, err
		}
		switch decision.Kind {
		case policy.DecisionAllow:
			return wasmext.DecisionAllow, nil
		case policy.DecisionRequireApproval:
			return wasmext.DecisionRequireApproval, nil
		default:
			return wasmext.DecisionDeny, nil
		}
	})
}

func configuredWASMSettings(cfg runtimeconfig.Config, options CompositionOptions) (map[string]string, error) {
	settings := cloneStringMap(cfg.Extensions["wasm"])
	for key, value := range options.ExtensionSettings["wasm"] {
		settings[key] = value
	}
	if options.DisabledExtensions["wasm"] {
		settings["enabled_ids"] = ""
	}
	ids := splitConfigList(settings["enabled_ids"])
	if len(ids) == 0 {
		return settings, nil
	}
	if !wasmext.HostAvailable() {
		return nil, fmt.Errorf("WASM extensions %v are enabled, but this binary was built without feature_wasm_ext", ids)
	}
	if _, err := wasmext.ParseCapabilities(splitConfigList(settings["allowed_capabilities"])); err != nil {
		return nil, fmt.Errorf("WASM allowed_capabilities: %w", err)
	}
	return settings, nil
}

func validateWASMSelection(ctx context.Context, cfg runtimeconfig.Config, options CompositionOptions) error {
	settings, err := configuredWASMSettings(cfg, options)
	if err != nil {
		return err
	}
	ids := splitConfigList(settings["enabled_ids"])
	if len(ids) == 0 {
		return nil
	}
	allowed, err := wasmext.ParseCapabilities(splitConfigList(settings["allowed_capabilities"]))
	if err != nil {
		return fmt.Errorf("WASM allowed_capabilities: %w", err)
	}
	manager := wasmext.NewManager(wasmext.Config{
		ExtensionDirs: splitConfigList(settings["directories"]), AllowedCapabilities: allowed,
		Policy: wasmext.AllowAllPolicy(),
	})
	defer func() { _ = manager.Close(context.Background()) }()
	if err := manager.Discover(ctx); err != nil {
		return fmt.Errorf("validate WASM extensions: %w", err)
	}
	preflightHost := extensions.NewHost(nil)
	defer func() { _ = preflightHost.Close(context.Background()) }()
	definitions, err := wasmext.ComponentDefinitions(manager, preflightHost, ids...)
	if err != nil {
		return err
	}
	graph := make([]component.Definition, 0, len(definitions))
	for _, definition := range definitions {
		info, err := manager.Get(strings.TrimPrefix(definition.ID, "wasm."))
		if err != nil {
			return err
		}
		if err := (wasmext.Config{HostVersion: buildinfo.Current().Version}).ValidateManifest(info.Manifest); err != nil {
			return err
		}
		graph = append(graph, component.Define(definition.Descriptor, func() (component.Component, error) {
			return component.ComponentFunc(func(context.Context, *component.Scope) error { return nil }), nil
		}))
	}
	if _, err := component.NewRuntime(graph...); err != nil {
		return fmt.Errorf("validate WASM component dependencies: %w", err)
	}
	return nil
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

func memoryDefinition(id, revision, hostProvider, providerID string, settings map[string]string, options CompositionOptions) component.Definition {
	descriptor := component.Descriptor{
		ID: id, Version: builtinComponentVersion, Revision: revision,
		Requirements: []component.ServiceRequirement{
			serviceRequirement(publicruntime.ExtensionHostKey, hostProvider),
			serviceRequirement(publicruntime.ProviderKey, providerID),
		},
	}
	return component.Define(descriptor, func() (component.Component, error) {
		return component.ComponentFunc(func(ctx context.Context, scope *component.Scope) error {
			host, err := component.Require(scope, publicruntime.ExtensionHostKey)
			if err != nil {
				return err
			}
			provider, err := component.Require(scope, publicruntime.ProviderKey)
			if err != nil {
				return err
			}
			memoryConfig, err := memoryext.ConfigFromSettings(settings, os.Getenv)
			if err != nil {
				return err
			}
			if memoryConfig.WorkspaceID == "" {
				memoryConfig.WorkspaceID = options.CWD
			}
			if memoryConfig.ProjectID == "" {
				memoryConfig.ProjectID = options.CWD
			}
			extOptions := []memoryext.Option{
				memoryext.WithConfig(memoryConfig),
				memoryext.WithExtractor(memory.NewProviderExtractor(provider, ai.Model{ID: options.Model})),
			}
			if memoryConfig.Enabled && memoryConfig.Profile != "stateless" && memoryConfig.Profile != "ephemeral" && memoryConfig.Backend == "filesystem" {
				directory := memoryConfig.Directory
				if directory == "" {
					if root := os.Getenv("Y_CODING_AGENT_DIR"); root != "" {
						directory = filepath.Join(root, "memory")
					} else {
						directory = filepath.Join(storage.DefaultAgentDir(), "memory")
					}
				}
				store, err := memory.NewFilesystemStore(directory)
				if err != nil {
					return err
				}
				extOptions = append(extOptions, memoryext.WithStore(store))
			} else if memoryConfig.Profile == "stateless" || memoryConfig.Profile == "ephemeral" || memoryConfig.Backend == "noop" || memoryConfig.Backend == "memory" {
				extOptions = append(extOptions, memoryext.WithStore(memoryext.NoopStore{}))
			}
			extension, err := memoryext.NewWithContext(ctx, extOptions...)
			if err != nil {
				return err
			}
			if err := host.InstallContext(ctx, extension); err != nil {
				_ = extension.Close(context.WithoutCancel(ctx))
				return fmt.Errorf("install extension %q: %w", extension.ID(), err)
			}
			if err := scope.Track(func(cleanupCtx context.Context) error { return host.Uninstall(cleanupCtx, extension.ID()) }); err != nil {
				_ = host.Uninstall(context.WithoutCancel(ctx), extension.ID())
				return err
			}
			host.AddAgentOption(agent.WithContextIdentity(memoryConfig.TenantID, memoryConfig.WorkspaceID, memoryConfig.ProjectID, "", ""))
			return nil
		}), nil
	})
}

func sessionStoreDefinition(id, revision, directory string) component.Definition {
	descriptor := component.Descriptor{ID: id, Version: builtinComponentVersion, Revision: revision, Provides: []component.ServiceRef{component.Reference(publicruntime.SessionStoreKey)}}
	return component.Define(descriptor, func() (component.Component, error) {
		return component.ComponentFunc(func(_ context.Context, scope *component.Scope) error {
			return component.Provide(scope, publicruntime.SessionStoreKey, session.Store(storage.NewSessionStore(directory)))
		}), nil
	})
}

func subagentsDefinition(id, revision, providerID, registryProvider, hostProvider string) component.Definition {
	descriptor := component.Descriptor{
		ID: id, Version: builtinComponentVersion, Revision: revision,
		Requirements: []component.ServiceRequirement{
			serviceRequirement(publicruntime.ProviderKey, providerID),
			serviceRequirement(publicruntime.ToolRegistryKey, registryProvider),
			serviceRequirement(publicruntime.ExtensionHostKey, hostProvider),
		},
	}
	return component.Define(descriptor, func() (component.Component, error) {
		return component.ComponentFunc(func(ctx context.Context, scope *component.Scope) error {
			provider, err := component.Require(scope, publicruntime.ProviderKey)
			if err != nil {
				return err
			}
			registry, err := component.Require(scope, publicruntime.ToolRegistryKey)
			if err != nil {
				return err
			}
			host, err := component.Require(scope, publicruntime.ExtensionHostKey)
			if err != nil {
				return err
			}
			manager, err := subagents.New(subagents.ManagerConfig{Provider: provider, Registry: registry})
			if err != nil {
				return err
			}
			if err := scope.Track(manager.Close); err != nil {
				_ = manager.Close(context.WithoutCancel(ctx))
				return err
			}
			if err := manager.RegisterScoped(registry, scope); err != nil {
				return err
			}
			host.AddAgentOption(manager.AgentOption())
			return nil
		}), nil
	})
}

func taskManagerDefinition(id, revision, registryProvider, hostProvider string, settings map[string]string) component.Definition {
	descriptor := component.Descriptor{
		ID: id, Version: builtinComponentVersion, Revision: revision,
		Requirements: []component.ServiceRequirement{
			serviceRequirement(publicruntime.ToolRegistryKey, registryProvider),
			serviceRequirement(publicruntime.ExtensionHostKey, hostProvider),
		},
	}
	return component.Define(descriptor, func() (component.Component, error) {
		return component.ComponentFunc(func(_ context.Context, scope *component.Scope) error {
			registry, err := component.Require(scope, publicruntime.ToolRegistryKey)
			if err != nil {
				return err
			}
			host, err := component.Require(scope, publicruntime.ExtensionHostKey)
			if err != nil {
				return err
			}
			managerOptions := make([]taskmanager.Option, 0, 1)
			if raw := settings["max_tasks"]; raw != "" {
				maxTasks, err := strconv.Atoi(raw)
				if err != nil || maxTasks <= 0 {
					return fmt.Errorf("task_manager setting max_tasks must be a positive integer")
				}
				managerOptions = append(managerOptions, taskmanager.WithMaxTasks(maxTasks))
			}
			manager := taskmanager.New(managerOptions...)
			if err := scope.Track(func(context.Context) error { manager.Reset(); return nil }); err != nil {
				return err
			}
			if err := manager.RegisterScoped(registry, scope); err != nil {
				return err
			}
			host.AddAgentOption(manager.AgentOption())
			return nil
		}), nil
	})
}

func serviceRequirement[T any](key component.Key[T], providerID string) component.ServiceRequirement {
	return component.ServiceRequirement{
		ServiceID:   component.Reference(key).ID,
		ProviderID:  providerID,
		Version:     component.VersionConstraint{Exact: component.Reference(key).Version},
		Cardinality: component.CardinalityOne,
	}
}

func binding(cfg runtimeconfig.Config, serviceID string) (string, error) {
	providerID := cfg.Bindings[serviceID]
	if providerID == "" {
		return "", fmt.Errorf("runtime composition: binding %q is required", serviceID)
	}
	return providerID, nil
}

func legacyComposition(cfg runtimeconfig.Config) runtimeconfig.Config {
	cfg.Components = map[string]runtimeconfig.ComponentConfig{
		"provider":   {Type: "provider.auto", Enabled: true},
		"policy":     {Type: "policy.default", Enabled: true},
		"telemetry":  {Type: "telemetry.default", Enabled: true},
		"tools":      {Type: "tools.default", Enabled: true},
		"extensions": {Type: "extensions.host", Enabled: true},
		"memory":     {Type: "extensions.memory", Enabled: true, Settings: cloneStringMap(cfg.Extensions["memory"])},
		"sessions":   {Type: "sessions.filesystem", Enabled: true},
	}
	if len(cfg.Providers) == 1 {
		for id, enabled := range cfg.Providers {
			if enabled {
				cfg.Components["provider"] = runtimeconfig.ComponentConfig{Type: "provider." + id, Enabled: true}
			}
		}
	}
	cfg.Bindings = map[string]string{
		"agent_provider": "provider", "policy_engine": "policy", "telemetry_emitter": "telemetry",
		"tools_registry": "tools", "extensions_host": "extensions", "sessions_store": "sessions",
	}
	return cfg
}

func applyRuntimeOverrides(cfg *runtimeconfig.Config, options CompositionOptions) {
	if cfg.Components == nil {
		cfg.Components = make(map[string]runtimeconfig.ComponentConfig)
	}
	if cfg.Bindings == nil {
		cfg.Bindings = make(map[string]string)
	}
	for id, entry := range cfg.Components {
		if entry.Type != "extensions.memory" {
			continue
		}
		if options.DisabledExtensions["memory"] {
			if entry.Settings == nil {
				entry.Settings = make(map[string]string)
			}
			entry.Settings["enabled"] = "false"
		}
		for key, value := range options.ExtensionSettings["memory"] {
			if entry.Settings == nil {
				entry.Settings = make(map[string]string)
			}
			entry.Settings[key] = value
		}
		cfg.Components[id] = entry
	}
}

func hasEnabledType(cfg runtimeconfig.Config, componentType string) bool {
	for _, entry := range cfg.Components {
		if entry.Enabled && entry.Type == componentType {
			return true
		}
	}
	return false
}

func hasEnabledProvider(cfg runtimeconfig.Config) bool {
	for _, entry := range cfg.Components {
		if entry.Enabled && strings.HasPrefix(entry.Type, "provider.") {
			return true
		}
	}
	return false
}

func sortedComponentIDs(components map[string]runtimeconfig.ComponentConfig) []string {
	ids := make([]string, 0, len(components))
	for id := range components {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func componentRevision(entry runtimeconfig.ComponentConfig) (string, error) {
	value := struct {
		Type     string
		Version  string
		Settings map[string]string
	}{Type: entry.Type, Version: entry.Version, Settings: entry.Settings}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func cloneStringMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
