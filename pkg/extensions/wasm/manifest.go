package wasm

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SupportedAPIVersion is the host-side ABI version recognised by Manifest
// validation. Manifests that declare a different api_version are rejected so
// that the host can fail fast on incompatible guests.
const SupportedAPIVersion = "pi.wasm.v1"

// ManifestFileName is the filename inspected during discovery.
const ManifestFileName = "extension.toml"

// Manifest captures the metadata read from extension.toml. Field semantics
// follow the spec in extension-wasm.md §7.
type Manifest struct {
	ID           string
	Name         string
	Version      string
	APIVersion   string
	Entry        string
	Runtime      RuntimeManifest
	Capabilities CapabilitySet
	Tools        []ToolManifest
}

// RuntimeManifest carries optional execution limits requested by the
// extension. Zero values mean "use host defaults".
type RuntimeManifest struct {
	MinYVersion string
	MemoryPages uint32
	TimeoutMS   uint32
	Fuel        uint64
}

// CapabilitySet enumerates the privileges requested by an extension. They are
// all denied by default and only granted when the host config opts in.
type CapabilitySet struct {
	YTools     bool
	Filesystem bool
	Network    bool
	Process    bool
	Git        bool
	Secrets    bool
	Storage    bool
	Logs       bool
}

// ToolManifest describes a tool the extension exports through the host.
type ToolManifest struct {
	Name        string
	Description string
}

// ReadManifest loads and validates a manifest from disk.
func ReadManifest(path string) (Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, &ManifestError{Path: path, Message: "cannot open manifest", Cause: err}
	}
	defer f.Close()

	m, err := parseManifest(path, f)
	if err != nil {
		return Manifest{}, err
	}
	if err := m.Validate(path); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Parse reads a manifest from the supplied reader without validation.
//
// The path argument is only used to enrich errors; pass the on-disk filename
// when available.
func Parse(path string, r io.Reader) (Manifest, error) {
	return parseManifest(path, r)
}

// Validate runs structural checks on the manifest and returns the first
// failure. The path argument enriches error messages.
func (m Manifest) Validate(path string) error {
	if strings.TrimSpace(m.ID) == "" {
		return newManifestError(path, "id", "must be a non-empty string", 0, nil)
	}
	if !validIdentifier(m.ID) {
		return newManifestError(path, "id", "must contain only letters, digits, '.', '_' or '-'", 0, nil)
	}
	if strings.TrimSpace(m.Name) == "" {
		return newManifestError(path, "name", "must be a non-empty string", 0, nil)
	}
	if strings.TrimSpace(m.Version) == "" {
		return newManifestError(path, "version", "must be a non-empty string", 0, nil)
	}
	if m.APIVersion != SupportedAPIVersion {
		return newManifestError(path, "api_version",
			fmt.Sprintf("expected %q, got %q", SupportedAPIVersion, m.APIVersion), 0, nil)
	}
	if strings.TrimSpace(m.Entry) == "" {
		return newManifestError(path, "entry", "must point to a .wasm file", 0, nil)
	}
	if filepath.IsAbs(m.Entry) || strings.Contains(filepath.ToSlash(m.Entry), "..") {
		return newManifestError(path, "entry", "must be a relative path inside the extension directory", 0, nil)
	}
	if !strings.HasSuffix(strings.ToLower(m.Entry), ".wasm") {
		return newManifestError(path, "entry", "must reference a .wasm file", 0, nil)
	}
	if m.Runtime.MemoryPages > maxMemoryPages {
		return newManifestError(path, "runtime.memory_pages",
			fmt.Sprintf("must be <= %d", maxMemoryPages), 0, nil)
	}
	if m.Runtime.TimeoutMS > maxTimeoutMS {
		return newManifestError(path, "runtime.timeout_ms",
			fmt.Sprintf("must be <= %d", maxTimeoutMS), 0, nil)
	}
	seen := make(map[string]struct{})
	for i, tool := range m.Tools {
		if strings.TrimSpace(tool.Name) == "" {
			return newManifestError(path, fmt.Sprintf("tools[%d].name", i), "must be a non-empty string", 0, nil)
		}
		if !validIdentifier(tool.Name) {
			return newManifestError(path, fmt.Sprintf("tools[%d].name", i),
				"must contain only letters, digits, '_' or '-'", 0, nil)
		}
		if _, dup := seen[tool.Name]; dup {
			return newManifestError(path, fmt.Sprintf("tools[%d].name", i),
				fmt.Sprintf("duplicate tool name %q", tool.Name), 0, nil)
		}
		seen[tool.Name] = struct{}{}
	}
	return nil
}

const (
	maxMemoryPages = 4096    // 256 MiB at 64 KiB/page; matches the host hard limit.
	maxTimeoutMS   = 600_000 // 10 minutes is enough for almost any tool.
)

// parseManifest is a minimal TOML reader scoped to manifest needs. It accepts
// scalar key/value pairs at the top level, [runtime]/[capabilities] tables,
// and [[tools]] array tables.
func parseManifest(path string, r io.Reader) (Manifest, error) {
	m := Manifest{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)

	const (
		stateTop = iota
		stateRuntime
		stateCapabilities
		stateTool
	)
	state := stateTop
	currentTool := -1

	for lineNo := 1; scanner.Scan(); lineNo++ {
		raw := stripTOMLComment(scanner.Text())
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[[") {
			if !strings.HasSuffix(line, "]]") {
				return Manifest{}, newManifestError(path, "", "invalid array-table header", lineNo, nil)
			}
			name := strings.TrimSpace(line[2 : len(line)-2])
			if name != "tools" {
				return Manifest{}, newManifestError(path, name,
					fmt.Sprintf("unsupported array table %q", name), lineNo, nil)
			}
			m.Tools = append(m.Tools, ToolManifest{})
			currentTool = len(m.Tools) - 1
			state = stateTool
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return Manifest{}, newManifestError(path, "", "invalid section header", lineNo, nil)
			}
			name := strings.TrimSpace(line[1 : len(line)-1])
			switch name {
			case "runtime":
				state = stateRuntime
			case "capabilities":
				state = stateCapabilities
			default:
				return Manifest{}, newManifestError(path, name,
					fmt.Sprintf("unsupported section %q", name), lineNo, nil)
			}
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Manifest{}, newManifestError(path, "", "expected key = value", lineNo, nil)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !validBareKey(key) {
			return Manifest{}, newManifestError(path, key, "invalid key", lineNo, nil)
		}

		switch state {
		case stateTop:
			if err := assignTopLevel(&m, key, value, lineNo, path); err != nil {
				return Manifest{}, err
			}
		case stateRuntime:
			if err := assignRuntime(&m.Runtime, key, value, lineNo, path); err != nil {
				return Manifest{}, err
			}
		case stateCapabilities:
			if err := assignCapability(&m.Capabilities, key, value, lineNo, path); err != nil {
				return Manifest{}, err
			}
		case stateTool:
			if currentTool < 0 {
				return Manifest{}, newManifestError(path, key,
					"key declared outside of a [[tools]] block", lineNo, nil)
			}
			if err := assignTool(&m.Tools[currentTool], key, value, lineNo, path); err != nil {
				return Manifest{}, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Manifest{}, &ManifestError{Path: path, Message: "read failed", Cause: err}
	}
	return m, nil
}

func assignTopLevel(m *Manifest, key, value string, line int, path string) error {
	switch key {
	case "id":
		s, err := parseString(value)
		if err != nil {
			return newManifestError(path, key, err.Error(), line, err)
		}
		m.ID = s
	case "name":
		s, err := parseString(value)
		if err != nil {
			return newManifestError(path, key, err.Error(), line, err)
		}
		m.Name = s
	case "version":
		s, err := parseString(value)
		if err != nil {
			return newManifestError(path, key, err.Error(), line, err)
		}
		m.Version = s
	case "api_version":
		s, err := parseString(value)
		if err != nil {
			return newManifestError(path, key, err.Error(), line, err)
		}
		m.APIVersion = s
	case "entry":
		s, err := parseString(value)
		if err != nil {
			return newManifestError(path, key, err.Error(), line, err)
		}
		m.Entry = s
	default:
		return newManifestError(path, key,
			fmt.Sprintf("unknown top-level key %q", key), line, nil)
	}
	return nil
}

func assignRuntime(r *RuntimeManifest, key, value string, line int, path string) error {
	switch key {
	case "min_pi_version", "min_y_version":
		s, err := parseString(value)
		if err != nil {
			return newManifestError(path, "runtime."+key, err.Error(), line, err)
		}
		r.MinYVersion = s
	case "memory_pages":
		n, err := parseUint(value, 32)
		if err != nil {
			return newManifestError(path, "runtime."+key, err.Error(), line, err)
		}
		r.MemoryPages = uint32(n)
	case "timeout_ms":
		n, err := parseUint(value, 32)
		if err != nil {
			return newManifestError(path, "runtime."+key, err.Error(), line, err)
		}
		r.TimeoutMS = uint32(n)
	case "fuel":
		n, err := parseUint(value, 64)
		if err != nil {
			return newManifestError(path, "runtime."+key, err.Error(), line, err)
		}
		r.Fuel = n
	default:
		return newManifestError(path, "runtime."+key,
			fmt.Sprintf("unknown runtime key %q", key), line, nil)
	}
	return nil
}

func assignCapability(c *CapabilitySet, key, value string, line int, path string) error {
	v, err := parseBool(value)
	if err != nil {
		return newManifestError(path, "capabilities."+key, err.Error(), line, err)
	}
	switch key {
	case "pi_tools", "y_tools":
		c.YTools = v
	case "filesystem":
		c.Filesystem = v
	case "network":
		c.Network = v
	case "process":
		c.Process = v
	case "git":
		c.Git = v
	case "secrets":
		c.Secrets = v
	case "storage":
		c.Storage = v
	case "logs":
		c.Logs = v
	default:
		return newManifestError(path, "capabilities."+key,
			fmt.Sprintf("unknown capability %q", key), line, nil)
	}
	return nil
}

func assignTool(t *ToolManifest, key, value string, line int, path string) error {
	switch key {
	case "name":
		s, err := parseString(value)
		if err != nil {
			return newManifestError(path, "tools."+key, err.Error(), line, err)
		}
		t.Name = s
	case "description":
		s, err := parseString(value)
		if err != nil {
			return newManifestError(path, "tools."+key, err.Error(), line, err)
		}
		t.Description = s
	case "input_schema", "output_schema":
		// Schemas are accepted but ignored at this stage; ABI work attaches
		// them later. Validate the value type so typos surface here.
		if _, err := parseString(value); err != nil {
			return newManifestError(path, "tools."+key, err.Error(), line, err)
		}
	default:
		return newManifestError(path, "tools."+key,
			fmt.Sprintf("unknown tool key %q", key), line, nil)
	}
	return nil
}

func parseString(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", fmt.Errorf("expected a double-quoted string")
	}
	body := value[1 : len(value)-1]
	if strings.Contains(body, "\\") {
		return "", fmt.Errorf("escape sequences are not supported")
	}
	return body, nil
}

func parseBool(value string) (bool, error) {
	switch strings.TrimSpace(value) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("expected a boolean (true/false)")
	}
}

func parseUint(value string, bits int) (uint64, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(value), 10, bits)
	if err != nil {
		return 0, fmt.Errorf("expected a non-negative integer")
	}
	return n, nil
}

func stripTOMLComment(line string) string {
	inString := false
	for i, r := range line {
		switch r {
		case '"':
			inString = !inString
		case '#':
			if !inString {
				return line[:i]
			}
		}
	}
	return line
}

func validBareKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

func validIdentifier(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}
