package wasm

import (
	"bufio"
	"encoding/json"
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
const SupportedAPIVersion = "y.wasm.v1"

// ManifestFileName is the filename inspected during discovery.
const ManifestFileName = "extension.toml"

// Manifest captures the metadata read from extension.toml. Field semantics
// follow the Y extension protocol.
type Manifest struct {
	ID           string
	Name         string
	Version      string
	APIVersion   string
	Entry        string
	Runtime      RuntimeManifest
	Capabilities CapabilitySet
	Tools        []ToolManifest
	Requirements []ServiceRequirementManifest
	Provides     []ServiceProvisionManifest
}

// ServiceRequirementManifest declares a dependency on a versioned host service.
type ServiceRequirementManifest struct {
	ServiceID  string
	ProviderID string
	Version    string
}

// ServiceProvisionManifest declares a JSON-mediated service exported by the guest.
type ServiceProvisionManifest struct {
	ServiceID string
	Version   string
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
	Name         string
	Description  string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
}

// ReadManifest loads and validates a manifest from disk.
func ReadManifest(path string) (Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, &ManifestError{Path: path, Message: "cannot open manifest", Cause: err}
	}
	defer func() { _ = f.Close() }()

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
	seenServices := make(map[string]struct{}, len(m.Requirements)+len(m.Provides))
	for i, requirement := range m.Requirements {
		if requirement.ServiceID == "" || requirement.ProviderID == "" || requirement.Version == "" {
			return newManifestError(path, fmt.Sprintf("requires[%d]", i), "service_id, provider_id, and version are required", 0, nil)
		}
		if _, exists := seenServices[requirement.ServiceID]; exists {
			return newManifestError(path, fmt.Sprintf("requires[%d].service_id", i), "duplicate service declaration", 0, nil)
		}
		seenServices[requirement.ServiceID] = struct{}{}
	}
	for i, provision := range m.Provides {
		if provision.ServiceID == "" || provision.Version == "" {
			return newManifestError(path, fmt.Sprintf("provides[%d]", i), "service_id and version are required", 0, nil)
		}
		if _, exists := seenServices[provision.ServiceID]; exists {
			return newManifestError(path, fmt.Sprintf("provides[%d].service_id", i), "duplicate service declaration", 0, nil)
		}
		seenServices[provision.ServiceID] = struct{}{}
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
		stateRequirement
		stateProvision
	)
	state := stateTop
	currentTool := -1
	currentRequirement := -1
	currentProvision := -1

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
			switch name {
			case "tools":
				m.Tools = append(m.Tools, ToolManifest{})
				currentTool = len(m.Tools) - 1
				state = stateTool
			case "requires":
				m.Requirements = append(m.Requirements, ServiceRequirementManifest{})
				currentRequirement = len(m.Requirements) - 1
				state = stateRequirement
			case "provides":
				m.Provides = append(m.Provides, ServiceProvisionManifest{})
				currentProvision = len(m.Provides) - 1
				state = stateProvision
			default:
				return Manifest{}, newManifestError(path, name,
					fmt.Sprintf("unsupported array table %q", name), lineNo, nil)
			}
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
		case stateRequirement:
			if err := assignServiceRequirement(&m.Requirements[currentRequirement], key, value, lineNo, path); err != nil {
				return Manifest{}, err
			}
		case stateProvision:
			if err := assignServiceProvision(&m.Provides[currentProvision], key, value, lineNo, path); err != nil {
				return Manifest{}, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Manifest{}, &ManifestError{Path: path, Message: "read failed", Cause: err}
	}
	return m, nil
}

func assignServiceRequirement(requirement *ServiceRequirementManifest, key, value string, line int, path string) error {
	parsed, err := parseString(value)
	if err != nil {
		return newManifestError(path, "requires."+key, err.Error(), line, err)
	}
	switch key {
	case "service_id":
		requirement.ServiceID = parsed
	case "provider_id":
		requirement.ProviderID = parsed
	case "version":
		requirement.Version = parsed
	default:
		return newManifestError(path, "requires."+key, fmt.Sprintf("unknown requirement key %q", key), line, nil)
	}
	return nil
}

func assignServiceProvision(provision *ServiceProvisionManifest, key, value string, line int, path string) error {
	parsed, err := parseString(value)
	if err != nil {
		return newManifestError(path, "provides."+key, err.Error(), line, err)
	}
	switch key {
	case "service_id":
		provision.ServiceID = parsed
	case "version":
		provision.Version = parsed
	default:
		return newManifestError(path, "provides."+key, fmt.Sprintf("unknown provision key %q", key), line, nil)
	}
	return nil
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
	case "min_y_version":
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
	case "y_tools":
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
		schema, err := parseString(value)
		if err != nil {
			return newManifestError(path, "tools."+key, err.Error(), line, err)
		}
		if !json.Valid([]byte(schema)) {
			return newManifestError(path, "tools."+key, "must be a valid JSON schema", line, nil)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(schema), &object); err != nil || object == nil {
			return newManifestError(path, "tools."+key, "must be a JSON object schema", line, err)
		}
		if key == "input_schema" {
			t.InputSchema = json.RawMessage(schema)
		} else {
			t.OutputSchema = json.RawMessage(schema)
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
	parsed, err := strconv.Unquote(value)
	if err != nil {
		return "", fmt.Errorf("expected a valid double-quoted string: %w", err)
	}
	return parsed, nil
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
