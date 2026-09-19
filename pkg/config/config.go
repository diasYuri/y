package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// CapabilityKind identifies a configurable capability namespace.
type CapabilityKind string

const (
	KindFeature  CapabilityKind = "feature"
	KindProvider CapabilityKind = "provider"
	KindTool     CapabilityKind = "tool"
	KindCommand  CapabilityKind = "command"
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
	Features    map[string]bool
	Providers   map[string]bool
	Tools       map[string]bool
	Limits      map[string]int64
	Extensions  map[string]map[string]string
	OfflineMode bool
	Telemetry   bool
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
// bare keys in [features], [providers], [tools], and [limits], plus generic
// extension sections such as [extensions.memory]. Extension values are kept as
// canonical strings so each extension owns its schema and validation.
func Parse(r io.Reader) (Config, error) {
	cfg := Config{
		Features:   make(map[string]bool),
		Providers:  make(map[string]bool),
		Tools:      make(map[string]bool),
		Limits:     make(map[string]int64),
		Extensions: make(map[string]map[string]string),
	}

	var section, extensionID string
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
			switch {
			case section == "features", section == "providers", section == "tools", section == "limits":
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
		default:
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
	return nil
}

// GenerateDefault returns default configuration file contents.
func GenerateDefault() string {
	return `# Y configuration file
# Enable/disable compiled features
[features]
fs = true
git = true
shell = true

# Enable/disable providers
[providers]
anthropic = true
openai = true
google = true
local = true

# Enable/disable tools
[tools]
read_file = true
write_file = true
list_files = true
search = true
edit = true
patch = true
run_command = true
git_status = true
git_diff = true
git_log = true
git_branch = true
git_checkout = true
git_commit = true

# Limits
[limits]
max_output_bytes = 1048576
max_file_read_bytes = 1048576
max_file_write_bytes = 1048576
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
