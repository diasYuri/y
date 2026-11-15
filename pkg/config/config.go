package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/diasYuri/y/pkg/component"
)

// CapabilityKind identifies a configurable capability namespace.
type CapabilityKind string

const (
	KindFeature   CapabilityKind = "feature"
	KindProvider  CapabilityKind = "provider"
	KindTool      CapabilityKind = "tool"
	KindCommand   CapabilityKind = "command"
	KindComponent CapabilityKind = "component"
)

// CapabilityRegistry describes the capabilities available to a runtime.
// Implementations may derive this information from build tags, plugins, or
// another deployment-specific mechanism.
type CapabilityRegistry interface {
	IsKnown(CapabilityKind, string) bool
	IsCompiled(CapabilityKind, string) bool
}

// Config is the declarative runtime configuration supported by y.
type Config struct {
	Features            map[string]bool
	Providers           map[string]bool
	Tools               map[string]bool
	Limits              map[string]int64
	Extensions          map[string]map[string]string
	Components          map[string]ComponentConfig
	Bindings            map[string]string
	CompositionExplicit bool
	OfflineMode         bool
	Telemetry           bool
}

// ComponentConfig selects one component instance in a runtime composition.
// Type identifies the compiled component definition; Version, when set, pins
// the exact descriptor version; Settings remain owned by the component.
type ComponentConfig struct {
	Type     string
	Enabled  bool
	Version  string
	Settings map[string]string
}

// HasExplicitComposition reports whether the config declared component or
// binding tables. It distinguishes an intentionally empty graph from legacy
// configuration that should be adapted to the built-in composition.
func (c Config) HasExplicitComposition() bool {
	return c.CompositionExplicit || len(c.Components) > 0 || len(c.Bindings) > 0
}

// Error describes a config parse or validation error.
type Error struct {
	Line    int
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("config line %d: %s", e.Line, e.Message)
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	return e.Cause
}

// LoadFile reads a TOML config file from path.
func LoadFile(path string) (Config, error) {
	return LoadFileWithLookup(path, os.Getenv)
}

// LoadFileWithLookup reads a configuration file and applies environment
// overrides using lookup. It is useful for deterministic application tests.
func LoadFileWithLookup(path string, lookup func(string) string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer func() { _ = f.Close() }()

	cfg, err := Parse(f)
	if err != nil {
		return Config{}, err
	}
	return ApplyEnvironment(cfg, lookup), nil
}

// Parse reads the subset of TOML used by y's declarative config. It supports
// bare keys in capability, limit, binding, and component tables, plus generic
// extension sections. Component settings are kept as canonical strings so each
// component owns its schema and validation.
func Parse(r io.Reader) (Config, error) {
	cfg := Config{
		Features:   make(map[string]bool),
		Providers:  make(map[string]bool),
		Tools:      make(map[string]bool),
		Limits:     make(map[string]int64),
		Extensions: make(map[string]map[string]string),
		Components: make(map[string]ComponentConfig),
		Bindings:   make(map[string]string),
	}

	var section, extensionID, componentID string
	var componentSettings bool
	scanner := bufio.NewScanner(r)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		raw := stripComment(scanner.Text())
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") || strings.Count(line, "[") != 1 || strings.Count(line, "]") != 1 {
				return Config{}, parseError(lineNo, "invalid section header", nil)
			}
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			extensionID = ""
			componentID = ""
			componentSettings = false
			switch {
			case section == "features", section == "providers", section == "tools", section == "limits":
			case section == "components":
				cfg.CompositionExplicit = true
			case section == "bindings":
				cfg.CompositionExplicit = true
			case strings.HasPrefix(section, "components."):
				parts := strings.Split(section, ".")
				if len(parts) < 2 || len(parts) > 3 || !validBareKey(parts[1]) || (len(parts) == 3 && parts[2] != "settings") {
					return Config{}, parseError(lineNo, fmt.Sprintf("invalid component section %q", section), nil)
				}
				cfg.CompositionExplicit = true
				componentID = parts[1]
				if _, exists := cfg.Components[componentID]; !exists {
					cfg.Components[componentID] = ComponentConfig{Enabled: true, Settings: make(map[string]string)}
				}
				componentSettings = len(parts) == 3
			case strings.HasPrefix(section, "extensions."):
				extensionID = strings.TrimPrefix(section, "extensions.")
				if !validBareKey(extensionID) || strings.Contains(extensionID, ".") {
					return Config{}, parseError(lineNo, fmt.Sprintf("invalid extension section %q", section), nil)
				}
				if cfg.Extensions[extensionID] == nil {
					cfg.Extensions[extensionID] = make(map[string]string)
				}
			default:
				return Config{}, parseError(lineNo, fmt.Sprintf("unsupported section %q", section), nil)
			}
			continue
		}

		if section == "" {
			return Config{}, parseError(lineNo, "key-value pair before any supported section", nil)
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, parseError(lineNo, "expected key = value", nil)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !validBareKey(key) {
			return Config{}, parseError(lineNo, fmt.Sprintf("invalid key %q", key), nil)
		}

		switch section {
		case "features":
			v, err := parseBool(value)
			if err != nil {
				return Config{}, parseError(lineNo, fmt.Sprintf("feature %q must be a boolean", key), err)
			}
			cfg.Features[key] = v
		case "providers":
			v, err := parseBool(value)
			if err != nil {
				return Config{}, parseError(lineNo, fmt.Sprintf("provider %q must be a boolean", key), err)
			}
			cfg.Providers[key] = v
		case "tools":
			v, err := parseBool(value)
			if err != nil {
				return Config{}, parseError(lineNo, fmt.Sprintf("tool %q must be a boolean", key), err)
			}
			cfg.Tools[key] = v
		case "limits":
			v, err := strconv.ParseInt(value, 10, 64)
			if err != nil || v < 0 {
				return Config{}, parseError(lineNo, fmt.Sprintf("limit %q must be a non-negative integer", key), err)
			}
			cfg.Limits[key] = v
		case "bindings":
			providerID, err := parseQuotedString(value)
			if err != nil || providerID == "" {
				return Config{}, parseError(lineNo, fmt.Sprintf("binding %q must name a component instance", key), err)
			}
			cfg.Bindings[key] = providerID
		default:
			if componentID != "" {
				componentConfig := cfg.Components[componentID]
				if componentSettings {
					parsed, err := parseExtensionScalar(value)
					if err != nil {
						return Config{}, parseError(lineNo, fmt.Sprintf("component %q setting %q must be a boolean, integer, or quoted string", componentID, key), err)
					}
					componentConfig.Settings[key] = parsed
					cfg.Components[componentID] = componentConfig
					continue
				}
				switch key {
				case "type":
					componentType, err := parseQuotedString(value)
					if err != nil || componentType == "" {
						return Config{}, parseError(lineNo, fmt.Sprintf("component %q type must be a non-empty quoted string", componentID), err)
					}
					componentConfig.Type = componentType
				case "version":
					version, err := parseQuotedString(value)
					if err != nil || version == "" {
						return Config{}, parseError(lineNo, fmt.Sprintf("component %q version must be a non-empty exact version", componentID), err)
					}
					componentConfig.Version = version
				case "enabled":
					enabled, err := parseBool(value)
					if err != nil {
						return Config{}, parseError(lineNo, fmt.Sprintf("component %q enabled must be a boolean", componentID), err)
					}
					componentConfig.Enabled = enabled
				default:
					return Config{}, parseError(lineNo, fmt.Sprintf("unsupported component key %q", key), nil)
				}
				cfg.Components[componentID] = componentConfig
				continue
			}
			if extensionID == "" {
				return Config{}, parseError(lineNo, fmt.Sprintf("unsupported section %q", section), nil)
			}
			parsed, err := parseExtensionScalar(value)
			if err != nil {
				return Config{}, parseError(lineNo, fmt.Sprintf("extension %q setting %q must be a boolean, integer, or quoted string", extensionID, key), err)
			}
			cfg.Extensions[extensionID][key] = parsed
		}
	}
	if err := scanner.Err(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// ApplyEnvironment applies runtime overrides not expressed in TOML.
func ApplyEnvironment(cfg Config, lookup func(string) string) Config {
	if lookup == nil {
		lookup = os.Getenv
	}
	if v := lookup("Y_OFFLINE"); v != "" {
		cfg.OfflineMode = parseEnvBool(v)
	}
	if v := lookup("Y_TELEMETRY"); v != "" {
		cfg.Telemetry = parseEnvBool(v)
	}
	return cfg
}

// Validate checks that enabled config entries are known and compiled into
// the supplied runtime.
func Validate(cfg Config, compiled CapabilityRegistry) error {
	if compiled == nil {
		return errors.New("capability registry is nil")
	}

	for id, enabled := range cfg.Features {
		if err := validateCapability(compiled, KindFeature, id, enabled); err != nil {
			return err
		}
	}
	for id, enabled := range cfg.Providers {
		if err := validateCapability(compiled, KindProvider, id, enabled); err != nil {
			return err
		}
	}
	for id, enabled := range cfg.Tools {
		if err := validateCapability(compiled, KindTool, id, enabled); err != nil {
			return err
		}
	}
	for id := range cfg.Limits {
		if !knownLimit(id) {
			return &Error{Message: fmt.Sprintf("unknown limit %q", id)}
		}
	}
	for instanceID, componentConfig := range cfg.Components {
		if !validBareKey(instanceID) {
			return &Error{Message: fmt.Sprintf("invalid component instance ID %q", instanceID)}
		}
		if componentConfig.Type == "" {
			return &Error{Message: fmt.Sprintf("component %q has no type", instanceID)}
		}
		if err := validateCapability(compiled, KindComponent, componentConfig.Type, componentConfig.Enabled); err != nil {
			return err
		}
		if componentConfig.Version != "" {
			if err := (component.VersionConstraint{Exact: componentConfig.Version}).Validate(); err != nil {
				return &Error{Message: fmt.Sprintf("component %q has invalid version %q: %v", instanceID, componentConfig.Version, err)}
			}
		}
		for key := range componentConfig.Settings {
			if !validBareKey(key) {
				return &Error{Message: fmt.Sprintf("component %q has invalid setting key %q", instanceID, key)}
			}
		}
	}
	for serviceID, providerID := range cfg.Bindings {
		if !validBareKey(serviceID) {
			return &Error{Message: fmt.Sprintf("invalid binding service ID %q", serviceID)}
		}
		provider, exists := cfg.Components[providerID]
		if !exists || !provider.Enabled {
			return &Error{Message: fmt.Sprintf("binding %q selects missing or disabled component %q", serviceID, providerID)}
		}
	}
	return nil
}

// GenerateDefault returns default configuration file contents.
func GenerateDefault() string {
	return `# Y configuration file
# The default composition is explicit and can be reproduced with the
# component versions and service bindings declared here.
[components.provider]
type = "provider.auto"
version = "1.0.0"
enabled = true

[components.policy]
type = "policy.default"
version = "1.0.0"
enabled = true

[components.telemetry]
type = "telemetry.default"
version = "1.0.0"
enabled = true

[components.tools]
type = "tools.default"
version = "1.0.0"
enabled = true

[components.extensions]
type = "extensions.host"
version = "1.0.0"
enabled = true

[components.memory]
type = "extensions.memory"
version = "1.0.0"
enabled = true

[components.sessions]
type = "sessions.filesystem"
version = "1.0.0"
enabled = true

[bindings]
agent_provider = "provider"
policy_engine = "policy"
telemetry_emitter = "telemetry"
tools_registry = "tools"
extensions_host = "extensions"
sessions_store = "sessions"

# Limits
[limits]
max_file_read_bytes = 1048576
max_command_output_bytes = 1048576
max_session_bytes = 8388608
max_parallel_tools = 8
command_timeout_seconds = 30

`
}

func validateCapability(reg CapabilityRegistry, kind CapabilityKind, id string, enabled bool) error {
	if !reg.IsKnown(kind, id) {
		return &Error{Message: fmt.Sprintf("unknown %s %q", kind, id)}
	}
	if enabled && !reg.IsCompiled(kind, id) {
		return &Error{Message: fmt.Sprintf("%s %q requested by config but not compiled into this binary", kind, id)}
	}
	return nil
}

func parseError(line int, message string, cause error) error {
	return &Error{Line: line, Message: message, Cause: cause}
}

func parseEnvBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return false
}

func parseBool(value string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean %q", value)
	}
}

func stripComment(line string) string {
	inString := false
	escaped := false
	for i, r := range line {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && inString:
			escaped = true
		case r == '"':
			inString = !inString
		case r == '#' && !inString:
			return line[:i]
		}
	}
	return line
}

func validBareKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func knownLimit(id string) bool {
	switch id {
	case "max_file_read_bytes",
		"max_command_output_bytes",
		"max_session_bytes",
		"max_parallel_tools",
		"command_timeout_seconds":
		return true
	default:
		return false
	}
}

func parseExtensionScalar(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return strconv.Unquote(value)
	}
	if _, err := parseBool(value); err == nil {
		return value, nil
	}
	if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
		return strconv.FormatInt(parsed, 10), nil
	}
	return "", fmt.Errorf("expected true, false, an integer, or a quoted string")
}

func parseQuotedString(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", fmt.Errorf("expected a quoted string")
	}
	return strconv.Unquote(value)
}
