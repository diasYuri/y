// Package config loads and validates y configuration files.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/yuri/y/internal/feature"
)

// Config is the declarative runtime configuration supported by the Go
// bootstrap. Runtime config can only enable capabilities compiled into the
// current binary.
type Config struct {
	Features    map[string]bool
	Providers   map[string]bool
	Tools       map[string]bool
	Limits      map[string]int64
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
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()

	return Parse(f)
}

// Parse reads the subset of TOML used by y's declarative config. It supports
// bare keys in [features], [providers], [tools], and [limits] with boolean or
// integer scalar values.
func Parse(r io.Reader) (Config, error) {
	cfg := Config{
		Features:  make(map[string]bool),
		Providers: make(map[string]bool),
		Tools:     make(map[string]bool),
		Limits:    make(map[string]int64),
	}

	var section string
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
			switch section {
			case "features", "providers", "tools", "limits":
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
		}
	}
	if err := scanner.Err(); err != nil {
		return Config{}, err
	}

	// Environment overrides for runtime flags not expressed in TOML.
	if v := os.Getenv("Y_OFFLINE"); v != "" {
		cfg.OfflineMode = parseEnvBool(v)
	}
	if v := os.Getenv("Y_TELEMETRY"); v != "" {
		cfg.Telemetry = parseEnvBool(v)
	}

	return cfg, nil
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

// Validate checks that enabled config entries are known and compiled into the
// current binary.
func Validate(cfg Config, compiled *feature.Registry) error {
	if compiled == nil {
		return errors.New("compiled feature registry is nil")
	}

	for id, enabled := range cfg.Features {
		if err := validateCapability(compiled, feature.KindFeature, id, enabled); err != nil {
			return err
		}
	}
	for id, enabled := range cfg.Providers {
		if err := validateCapability(compiled, feature.KindProvider, id, enabled); err != nil {
			return err
		}
	}
	for id, enabled := range cfg.Tools {
		if err := validateCapability(compiled, feature.KindTool, id, enabled); err != nil {
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

func validateCapability(reg *feature.Registry, kind feature.Kind, id string, enabled bool) error {
	if !feature.IsKnown(kind, id) {
		return &Error{Message: fmt.Sprintf("unknown %s %q", kind, id)}
	}
	if enabled && !reg.Has(kind, id) {
		return &Error{Message: fmt.Sprintf("%s %q requested by config but not compiled into this binary", kind, id)}
	}
	return nil
}

func parseError(line int, message string, cause error) error {
	return &Error{Line: line, Message: message, Cause: cause}
}

// GenerateDefault returns a default configuration file contents.
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
