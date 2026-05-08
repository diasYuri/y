package wasm

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Status describes the lifecycle position of an extension instance.
type Status string

const (
	// StatusDiscovered means the manifest has been read but the module has
	// not been instantiated yet.
	StatusDiscovered Status = "discovered"
	// StatusLoaded means the WASM module has been instantiated and is ready
	// to handle calls.
	StatusLoaded Status = "loaded"
	// StatusFailed means a previous load attempt failed; further calls will
	// retry instantiation unless the host disables the extension.
	StatusFailed Status = "failed"
)

// ExtensionInfo summarises a discovered extension for callers like CLI
// commands and policy gates.
type ExtensionInfo struct {
	Manifest Manifest
	Dir      string
	Status   Status
	LastErr  string
}

// ToolRequest is the input to Manager.CallTool. Arguments must already be
// JSON because the host will serialise them straight into the wire
// envelope.
type ToolRequest struct {
	Tool      string
	Arguments json.RawMessage
	// RequestID propagates a caller-side identifier into structured logs
	// and the guest envelope. It is optional.
	RequestID string
}

// ToolResponse is the structured payload returned from Manager.CallTool.
type ToolResponse struct {
	Content []ContentBlock
}

// HostInvoker is the optional interface a host registers to service guest
// tool_invoke host calls. The Manager forwards capability-checked
// tool_invoke envelopes to the invoker so y-side tools can be reused by
// extensions.
type HostInvoker interface {
	InvokeTool(ctx context.Context, extensionID, tool string, args json.RawMessage) (HostToolInvokeResult, error)
}

// HostInvokerFunc adapts a function into a HostInvoker.
type HostInvokerFunc func(ctx context.Context, extensionID, tool string, args json.RawMessage) (HostToolInvokeResult, error)

// InvokeTool implements HostInvoker.
func (f HostInvokerFunc) InvokeTool(ctx context.Context, extensionID, tool string, args json.RawMessage) (HostToolInvokeResult, error) {
	return f(ctx, extensionID, tool, args)
}

// LogSink is the interface used to consume guest log messages. Implementers
// are expected to truncate when the message is large because the host
// already enforces a quota per call.
type LogSink interface {
	Log(extensionID string, level LogLevel, message string)
}

// LogSinkFunc adapts a plain function into a LogSink.
type LogSinkFunc func(extensionID string, level LogLevel, message string)

// Log implements LogSink.
func (f LogSinkFunc) Log(extensionID string, level LogLevel, message string) {
	f(extensionID, level, message)
}

// Manager owns the lifecycle of WASM extensions. Implementations are
// build-tag-specific: with feature_wasm_ext the Manager instantiates modules
// via wazero, otherwise Load returns ErrHostUnavailable.
type Manager interface {
	// Discover scans the configured extension directories and registers any
	// valid manifests. Errors from individual extensions are stored on the
	// ExtensionInfo and do not abort the scan.
	Discover(ctx context.Context) error
	// List returns information about every discovered extension.
	List() []ExtensionInfo
	// Get returns the metadata of a single extension by id.
	Get(id string) (ExtensionInfo, error)
	// Load makes sure the named extension is instantiated and ready. The
	// Manager performs this lazily so callers can rely on Load to be a
	// no-op once the module is warm.
	Load(ctx context.Context, id string) error
	// Unload tears down an instantiated module. It is safe to call on an
	// extension that was never loaded.
	Unload(ctx context.Context, id string) error
	// CallTool dispatches a tool_call envelope to the named extension and
	// returns the structured response. Loads the module on demand and
	// enforces capability/limit checks.
	CallTool(ctx context.Context, id string, req ToolRequest) (ToolResponse, error)
	// Close releases all resources held by the Manager. It should be called
	// during shutdown.
	Close(ctx context.Context) error
}

// Config controls Manager construction.
type Config struct {
	// ExtensionDirs are scanned for manifests during Discover. ~ expansion
	// is performed by the caller; relative directories are resolved against
	// the workspace root.
	ExtensionDirs []string
	// LazyLoad keeps modules unloaded until Load or CallTool is invoked.
	// The default is true.
	LazyLoad bool
	// MaxLoadedModules caps the number of warm modules. Zero means no cap.
	MaxLoadedModules int
	// DefaultTimeoutMS bounds tool calls when the manifest does not specify
	// a timeout. Zero means use a hard-coded fallback.
	DefaultTimeoutMS uint32
	// DefaultMemoryPages bounds module memory when the manifest is silent.
	DefaultMemoryPages uint32
	// DefaultLimits provides the host-side fallback for output/log/host
	// call quotas. Zero fields fall back to DefaultLimits().
	DefaultLimits Limits
	// Policy is consulted before any host-call that touches a capability.
	// A nil policy is treated as DenyAllPolicy(): the Manager still
	// instantiates modules and runs tool calls, but every host_call
	// envelope is rejected.
	Policy Policy
	// AllowedCapabilities lists capabilities the host is willing to grant
	// at all. Manifest declarations are intersected with this list before
	// being applied. Nil grants every capability declared by the manifest.
	AllowedCapabilities []Capability
	// Invoker handles the y_tools capability by routing tool_invoke
	// envelopes back into the host. A nil invoker rejects tool_invoke
	// host calls with code "unsupported_host_op".
	Invoker HostInvoker
	// LogSink consumes guest logs. A nil sink discards them.
	LogSink LogSink
	// HostVersion is reported to guests during init. Set to the y
	// version string.
	HostVersion string
}

// DefaultConfig returns a baseline configuration suitable for tests.
func DefaultConfig() Config {
	return Config{
		LazyLoad:           true,
		MaxLoadedModules:   4,
		DefaultTimeoutMS:   5000,
		DefaultMemoryPages: 256,
		DefaultLimits:      DefaultLimits(),
	}
}

// resolveLimits combines the manager defaults with the manifest's runtime
// hints so the active host always knows the effective quotas.
func (c Config) resolveLimits(m Manifest) Limits {
	host := c.DefaultLimits
	if host.TimeoutMS == 0 && c.DefaultTimeoutMS != 0 {
		host.TimeoutMS = c.DefaultTimeoutMS
	}
	if host.MemoryPages == 0 && c.DefaultMemoryPages != 0 {
		host.MemoryPages = c.DefaultMemoryPages
	}
	merged := DefaultLimits().Apply(host)
	return limitForManifest(merged, m)
}

// resolvedPolicy returns a Policy that is never nil. The deny-all default
// keeps the host safe even when callers forget to wire a policy.
func (c Config) resolvedPolicy() Policy {
	if c.Policy != nil {
		return c.Policy
	}
	return DenyAllPolicy()
}

// resolvedAllowed returns the intersection allowlist, defaulting to every
// known capability so manifests do not have to declare them twice.
func (c Config) resolvedAllowed() []Capability {
	if c.AllowedCapabilities != nil {
		return c.AllowedCapabilities
	}
	return AllCapabilities()
}

// state holds the bookkeeping shared by every Manager implementation.
type state struct {
	mu    sync.RWMutex
	items map[string]*entry
	dirs  []string
}

type entry struct {
	info     ExtensionInfo
	dir      string
	manifest Manifest
}

func newState(cfg Config) *state {
	return &state{
		items: make(map[string]*entry),
		dirs:  append([]string(nil), cfg.ExtensionDirs...),
	}
}

// discover walks the configured directories looking for extension.toml
// manifests. It is shared by every Manager implementation so that the
// disabled stub still reports what would have been available.
func (s *state) discover(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make(map[string]*entry)

	var firstErr error
	for _, dir := range s.dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		stat, err := os.Stat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("scan %s: %w", dir, err)
			}
			continue
		}
		if !stat.IsDir() {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("read %s: %w", dir, err)
			}
			continue
		}
		for _, child := range entries {
			if !child.IsDir() {
				continue
			}
			extDir := filepath.Join(dir, child.Name())
			manifestPath := filepath.Join(extDir, ManifestFileName)
			if _, err := os.Stat(manifestPath); err != nil {
				continue
			}
			m, err := ReadManifest(manifestPath)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if existing, ok := s.items[m.ID]; ok {
				err := fmt.Errorf("duplicate extension id %q in %s and %s",
					m.ID, existing.dir, extDir)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			s.items[m.ID] = &entry{
				info: ExtensionInfo{
					Manifest: m,
					Dir:      extDir,
					Status:   StatusDiscovered,
				},
				dir:      extDir,
				manifest: m,
			}
		}
	}

	return firstErr
}

func (s *state) list() []ExtensionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ExtensionInfo, 0, len(s.items))
	for _, e := range s.items {
		out = append(out, e.info)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Manifest.ID < out[j].Manifest.ID
	})
	return out
}

func (s *state) get(id string) (ExtensionInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.items[id]
	if !ok {
		return ExtensionInfo{}, fmt.Errorf("%w: %q", ErrExtensionNotFound, id)
	}
	return e.info, nil
}

func (s *state) lookup(id string) (*entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.items[id]
	return e, ok
}

func (s *state) updateStatus(id string, status Status, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[id]
	if !ok {
		return
	}
	e.info.Status = status
	if err != nil {
		e.info.LastErr = err.Error()
	} else {
		e.info.LastErr = ""
	}
}

// EntryPath returns the absolute path to an extension's wasm module. It is
// exported because the active runtime layer needs it during lazy load.
func (i ExtensionInfo) EntryPath() string {
	if i.Dir == "" || i.Manifest.Entry == "" {
		return ""
	}
	return filepath.Join(i.Dir, i.Manifest.Entry)
}

// readEntry returns the bytes of an extension's wasm module. The function is
// internal so that the active runtime can validate paths through a single
// entry point.
func readEntry(dir, entry string) ([]byte, error) {
	path := filepath.Join(dir, entry)
	cleanRoot, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return nil, err
	}
	if rel == ".." || (len(rel) > 2 && rel[:3] == ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("entry %q escapes extension directory", entry)
	}
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("wasm module not found at %s", path)
		}
		return nil, err
	}
	return data, nil
}

func isNotFound(err error) bool {
	return os.IsNotExist(err) || err == fs.ErrNotExist
}
