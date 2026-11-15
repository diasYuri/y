//go:build feature_wasm_ext

package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// activeManager is the wazero-backed implementation used in builds compiled
// with the feature_wasm_ext tag. Runtimes are shared only by modules with
// the same memory ceiling, so loading order cannot weaken a guest limit.
type activeManager struct {
	cfg      Config
	state    *state
	runtimes map[uint32]wazero.Runtime
	loadMu   sync.Mutex

	mu       sync.Mutex
	loaded   map[string]*loadedModule
	retiring map[string]*loadedModule
	closed   bool
	loadOrd  []string
}

// loadedModule tracks a warm guest instance plus the resolved capability
// view used during host calls.
type loadedModule struct {
	info        ExtensionInfo
	module      api.Module
	limits      Limits
	grants      CapabilityGrantSet
	initialized bool
	// The export functions are cached after Load to avoid the lookup cost
	// on every tool call. They may be nil when the guest does not export
	// them; CallTool falls back to the alias names defined in the ABI.
	fnInit     api.Function
	fnHandle   api.Function
	fnShutdown api.Function
	fnFree     api.Function
	fnMalloc   api.Function
	execGate   chan struct{}
	callMu     sync.Mutex
	accepting  bool
	inFlight   int
	drained    chan struct{}
	closeMu    sync.Mutex
	moduleDone bool
	moduleErr  error
}

func (mod *loadedModule) acquireCall() bool {
	mod.callMu.Lock()
	defer mod.callMu.Unlock()
	if !mod.accepting {
		return false
	}
	mod.inFlight++
	return true
}

func (mod *loadedModule) releaseCall() {
	mod.callMu.Lock()
	defer mod.callMu.Unlock()
	mod.inFlight--
	mod.finishDrainLocked()
}

func (mod *loadedModule) retireCalls() <-chan struct{} {
	mod.callMu.Lock()
	defer mod.callMu.Unlock()
	mod.accepting = false
	mod.finishDrainLocked()
	return mod.drained
}

func (mod *loadedModule) finishDrainLocked() {
	if !mod.accepting && mod.inFlight == 0 && mod.drained != nil {
		select {
		case <-mod.drained:
		default:
			close(mod.drained)
		}
	}
}

func (m *activeManager) finalizeModule(ctx context.Context, mod *loadedModule) error {
	mod.closeMu.Lock()
	defer mod.closeMu.Unlock()
	if mod.moduleDone {
		return mod.moduleErr
	}
	mod.moduleErr = errors.Join(m.shutdownModule(ctx, mod), mod.module.Close(ctx))
	mod.moduleDone = true
	return mod.moduleErr
}

// NewManager constructs the wazero-backed Manager. Module instantiation is
// deferred until Load or CallTool is invoked.
func NewManager(cfg Config) Manager {
	if !cfg.LazyLoad && cfg.MaxLoadedModules == 0 {
		cfg.LazyLoad = true
	}
	return &activeManager{
		cfg:      cfg,
		state:    newState(cfg),
		loaded:   make(map[string]*loadedModule),
		retiring: make(map[string]*loadedModule),
	}
}

// HostAvailable reports whether this build links the WASM runtime.
func HostAvailable() bool { return true }

func (m *activeManager) Discover(ctx context.Context) error {
	return m.state.discover(ctx)
}

func (m *activeManager) List() []ExtensionInfo { return m.state.list() }

func (m *activeManager) Get(id string) (ExtensionInfo, error) {
	return m.state.get(id)
}

func (m *activeManager) Load(ctx context.Context, id string) error {
	m.loadMu.Lock()
	defer m.loadMu.Unlock()
	entry, ok := m.state.lookup(id)
	if !ok {
		return fmt.Errorf("%w: %q", ErrExtensionNotFound, id)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("wasm manager is closed")
	}
	if _, retiring := m.retiring[id]; retiring {
		m.mu.Unlock()
		return fmt.Errorf("WASM extension %q is retiring", id)
	}
	if _, alreadyLoaded := m.loaded[id]; alreadyLoaded {
		m.mu.Unlock()
		return nil
	}
	if m.cfg.MaxLoadedModules > 0 && len(m.loaded)+len(m.retiring) >= m.cfg.MaxLoadedModules {
		m.mu.Unlock()
		return fmt.Errorf("WASM extension %q: loaded module limit reached (%d)", id, m.cfg.MaxLoadedModules)
	}
	m.mu.Unlock()

	if err := m.cfg.ValidateManifest(entry.manifest); err != nil {
		return err
	}
	mod, err := m.instantiate(ctx, entry)
	if err != nil {
		m.state.updateStatus(id, StatusFailed, err)
		return err
	}

	if err := m.invokeInit(ctx, mod); err != nil {
		_ = mod.module.Close(ctx)
		m.state.updateStatus(id, StatusFailed, err)
		return err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = mod.module.Close(ctx)
		return errors.New("wasm manager is closed")
	}
	if _, alreadyLoaded := m.loaded[id]; alreadyLoaded {
		m.mu.Unlock()
		_ = mod.module.Close(ctx)
		return nil
	}
	m.loaded[id] = mod
	m.loadOrd = append(m.loadOrd, id)
	m.state.updateStatus(id, StatusLoaded, nil)
	m.mu.Unlock()
	return nil
}

func (m *activeManager) Unload(ctx context.Context, id string) error {
	if _, ok := m.state.lookup(id); !ok {
		return fmt.Errorf("%w: %q", ErrExtensionNotFound, id)
	}
	m.loadMu.Lock()
	m.mu.Lock()
	mod, ok := m.loaded[id]
	if ok {
		delete(m.loaded, id)
		m.removeFromOrder(id)
		m.retiring[id] = mod
	}
	if !ok {
		mod = m.retiring[id]
	}
	if mod == nil {
		m.mu.Unlock()
		m.loadMu.Unlock()
		return nil
	}
	drained := mod.retireCalls()
	m.mu.Unlock()
	m.loadMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-drained:
	case <-ctx.Done():
		return ctx.Err()
	}
	err := m.finalizeModule(ctx, mod)
	m.mu.Lock()
	if m.retiring[id] == mod {
		m.state.updateStatus(id, StatusDiscovered, err)
		delete(m.retiring, id)
	}
	m.mu.Unlock()
	return err
}

func (m *activeManager) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.loadMu.Lock()
	m.mu.Lock()
	if m.closed && len(m.runtimes) == 0 && len(m.retiring) == 0 {
		m.mu.Unlock()
		m.loadMu.Unlock()
		return nil
	}
	m.closed = true
	modsByID := make(map[string]*loadedModule, len(m.loaded)+len(m.retiring))
	for id, mod := range m.loaded {
		modsByID[id] = mod
		m.retiring[id] = mod
	}
	for id, mod := range m.retiring {
		modsByID[id] = mod
	}
	m.loaded = make(map[string]*loadedModule)
	m.loadOrd = nil
	drained := make(map[string]<-chan struct{}, len(modsByID))
	for id, mod := range modsByID {
		drained[id] = mod.retireCalls()
	}
	m.mu.Unlock()
	m.loadMu.Unlock()

	var firstErr error
	for id, mod := range modsByID {
		select {
		case <-drained[id]:
		case <-ctx.Done():
			return errors.Join(firstErr, ctx.Err())
		}
		if err := m.finalizeModule(ctx, mod); err != nil {
			firstErr = errors.Join(firstErr, err)
		}
		m.mu.Lock()
		if m.retiring[id] == mod {
			m.state.updateStatus(id, StatusDiscovered, mod.moduleErr)
			delete(m.retiring, id)
		}
		m.mu.Unlock()
	}
	m.mu.Lock()
	runtimes := m.runtimes
	m.runtimes = nil
	m.mu.Unlock()
	for _, rt := range runtimes {
		if err := rt.Close(ctx); err != nil {
			firstErr = errors.Join(firstErr, err)
		}
	}

	return firstErr
}

func (m *activeManager) shutdownModule(ctx context.Context, mod *loadedModule) error {
	if mod == nil || !mod.initialized || mod.fnShutdown == nil {
		return nil
	}
	raw, err := m.dispatch(ctx, mod, Envelope{
		APIVersion:  SupportedAPIVersion,
		ExtensionID: mod.info.Manifest.ID,
		Kind:        KindShutdown,
	}, mod.fnShutdown, ExportShutdown)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	response, err := UnmarshalResponse(raw)
	if err != nil {
		return newExtensionError(mod.info.Manifest.ID, CodeInternal, "decode shutdown response", err)
	}
	if !response.OK {
		if response.Error != nil {
			return newExtensionError(mod.info.Manifest.ID, response.Error.Code, response.Error.Message, nil)
		}
		return newExtensionError(mod.info.Manifest.ID, CodeInternal, "shutdown returned not ok", nil)
	}
	return nil
}

// CallTool invokes the named tool on the named extension. It loads the
// module on demand, fabricates the JSON envelope, enforces input/output
// limits and converts traps into structured errors instead of letting them
// propagate as panics.
func (m *activeManager) CallTool(ctx context.Context, id string, req ToolRequest) (resp ToolResponse, err error) {
	if _, ok := m.state.lookup(id); !ok {
		return ToolResponse{}, fmt.Errorf("%w: %q", ErrExtensionNotFound, id)
	}
	if err := m.Load(ctx, id); err != nil {
		return ToolResponse{}, err
	}
	m.mu.Lock()
	mod, ok := m.loaded[id]
	if ok {
		ok = mod.acquireCall()
	}
	m.mu.Unlock()
	if !ok {
		return ToolResponse{}, fmt.Errorf("%w: %q", ErrExtensionNotFound, id)
	}
	defer mod.releaseCall()

	defer func() {
		if r := recover(); r != nil {
			err = newExtensionError(id, CodeTrap,
				fmt.Sprintf("guest call panicked: %v", r), nil)
			resp = ToolResponse{}
			m.state.updateStatus(id, StatusFailed, err)
		}
	}()

	payload, marshalErr := json.Marshal(ToolCallRequest{Tool: req.Tool, Arguments: req.Arguments})
	if marshalErr != nil {
		return ToolResponse{}, newExtensionError(id, CodeInvalidArgument,
			"marshal tool arguments", marshalErr)
	}
	envelope := Envelope{
		APIVersion:  SupportedAPIVersion,
		RequestID:   req.RequestID,
		ExtensionID: id,
		Kind:        KindToolCall,
		Payload:     payload,
	}

	raw, err := m.dispatch(ctx, mod, envelope, mod.fnHandle, ExportHandle)
	if err != nil {
		return ToolResponse{}, err
	}

	parsed, err := UnmarshalResponse(raw)
	if err != nil {
		return ToolResponse{}, newExtensionError(id, CodeInternal,
			"decode guest response", err)
	}
	if !parsed.OK {
		if parsed.Error == nil {
			return ToolResponse{}, newExtensionError(id, CodeInternal,
				"guest reported failure without an error object", nil)
		}
		return ToolResponse{}, newExtensionError(id,
			parsed.Error.Code,
			parsed.Error.Message,
			nil)
	}
	if len(parsed.Payload) == 0 {
		return ToolResponse{}, nil
	}
	var tr ToolCallResponse
	if err := json.Unmarshal(parsed.Payload, &tr); err != nil {
		return ToolResponse{}, newExtensionError(id, CodeInternal,
			"decode guest tool payload", err)
	}
	return ToolResponse(tr), nil
}

// CallRuntime dispatches the P1 lifecycle/event/hook envelope through the
// guest's existing handle export. No new guest export is needed, preserving
// y.wasm.v1 modules that already route envelopes by Kind.
func (m *activeManager) CallRuntime(ctx context.Context, id string, req RuntimeRequest) (resp RuntimeResponse, err error) {
	switch req.Kind {
	case KindLifecycle, KindRuntimeEvent, KindHook:
	default:
		return RuntimeResponse{}, newExtensionError(id, CodeInvalidArgument, "unsupported runtime envelope kind", nil)
	}
	if _, ok := m.state.lookup(id); !ok {
		return RuntimeResponse{}, fmt.Errorf("%w: %q", ErrExtensionNotFound, id)
	}
	if err := m.Load(ctx, id); err != nil {
		return RuntimeResponse{}, err
	}
	m.mu.Lock()
	mod, ok := m.loaded[id]
	if ok {
		ok = mod.acquireCall()
	}
	m.mu.Unlock()
	if !ok {
		return RuntimeResponse{}, fmt.Errorf("%w: %q", ErrExtensionNotFound, id)
	}
	defer mod.releaseCall()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = newExtensionError(id, CodeTrap, fmt.Sprintf("guest runtime call panicked: %v", recovered), nil)
			resp = RuntimeResponse{}
			m.state.updateStatus(id, StatusFailed, err)
		}
	}()
	raw, err := m.dispatch(ctx, mod, Envelope{APIVersion: SupportedAPIVersion, RequestID: req.RequestID, ExtensionID: id, Kind: req.Kind, Payload: req.Payload}, mod.fnHandle, ExportHandle)
	if err != nil {
		return RuntimeResponse{}, err
	}
	parsed, err := UnmarshalResponse(raw)
	if err != nil {
		return RuntimeResponse{}, newExtensionError(id, CodeInternal, "decode guest runtime response", err)
	}
	if !parsed.OK {
		if parsed.Error == nil {
			return RuntimeResponse{}, newExtensionError(id, CodeInternal, "guest reported runtime failure without an error object", nil)
		}
		return RuntimeResponse{}, newExtensionError(id, parsed.Error.Code, parsed.Error.Message, nil)
	}
	return RuntimeResponse{Payload: append(json.RawMessage(nil), parsed.Payload...)}, nil
}

// invokeInit calls the guest's y_extension_init export. Failures fall back
// to a trap-safe error so the caller can decide whether to retry or unload.
func (m *activeManager) invokeInit(ctx context.Context, mod *loadedModule) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = newExtensionError(mod.info.Manifest.ID, CodeTrap,
				fmt.Sprintf("init panicked: %v", r), nil)
		}
	}()
	if mod.fnInit == nil {
		mod.initialized = true
		return nil
	}
	limits := mod.limits
	caps := mod.grants.Strings()
	payload, _ := json.Marshal(InitRequest{
		YVersion:     m.cfg.HostVersion,
		ExtensionID:  mod.info.Manifest.ID,
		Capabilities: caps,
		Limits:       limits.Snapshot(),
	})
	envelope := Envelope{
		APIVersion:  SupportedAPIVersion,
		ExtensionID: mod.info.Manifest.ID,
		Kind:        KindInit,
		Payload:     payload,
	}
	raw, err := m.dispatch(ctx, mod, envelope, mod.fnInit, ExportInit)
	if err != nil {
		return err
	}
	resp, err := UnmarshalResponse(raw)
	if err != nil {
		return newExtensionError(mod.info.Manifest.ID, CodeInternal,
			"decode init response", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			return newExtensionError(mod.info.Manifest.ID, resp.Error.Code,
				resp.Error.Message, nil)
		}
		return newExtensionError(mod.info.Manifest.ID, CodeInternal,
			"init returned not ok", nil)
	}
	mod.initialized = true
	return nil
}

// dispatch handles the trap-safe call sequence: marshal into guest
// memory, invoke the export, copy the response out, free guest memory.
func (m *activeManager) dispatch(ctx context.Context, mod *loadedModule, env Envelope, fn api.Function, fallbackName string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, mod.limits.Timeout())
	defer cancel()
	select {
	case mod.execGate <- struct{}{}:
		defer func() { <-mod.execGate }()
	case <-callCtx.Done():
		return nil, callCtx.Err()
	}
	if err := callCtx.Err(); err != nil {
		return nil, err
	}
	if fn == nil {
		fn = mod.module.ExportedFunction(fallbackName)
	}
	if fn == nil {
		return nil, newExtensionError(mod.info.Manifest.ID, CodeABIMismatch,
			fmt.Sprintf("guest does not export %q", fallbackName), nil)
	}

	encoded, err := MarshalEnvelope(env, mod.limits.MaxInputBytes)
	if err != nil {
		return nil, newExtensionError(mod.info.Manifest.ID, CodeInputTooLarge,
			err.Error(), err)
	}

	scope := newCallScope(mod, m.cfg)
	callCtx = context.WithValue(callCtx, callContextKey{}, scope)

	ptr, length, err := m.allocAndWrite(callCtx, mod, encoded)
	if err != nil {
		return nil, err
	}
	defer m.freeGuestBuffer(callCtx, mod, ptr, length)

	stack := []uint64{api.EncodeU32(ptr), api.EncodeU32(length)}
	if err := fn.CallWithStack(callCtx, stack); err != nil {
		return nil, classifyCallError(mod.info.Manifest.ID, err)
	}
	respPtr, respLen := UnpackPointerLength(stack[0])
	if respLen == 0 {
		return nil, nil
	}
	if mod.limits.MaxOutputBytes > 0 && respLen > mod.limits.MaxOutputBytes {
		// Free even when we refuse to read so guests cannot leak memory by
		// returning oversized buffers.
		m.freeGuestBuffer(callCtx, mod, respPtr, respLen)
		return nil, newExtensionError(mod.info.Manifest.ID, CodeOutputTooLarge,
			fmt.Sprintf("response %d bytes exceeds limit %d", respLen, mod.limits.MaxOutputBytes),
			nil)
	}
	mem := mod.module.Memory()
	if mem == nil {
		return nil, newExtensionError(mod.info.Manifest.ID, CodeABIMismatch,
			"guest does not export memory", nil)
	}
	buf, ok := mem.Read(respPtr, respLen)
	if !ok {
		return nil, newExtensionError(mod.info.Manifest.ID, CodeABIMismatch,
			"guest returned out-of-range pointer", nil)
	}
	out := make([]byte, len(buf))
	copy(out, buf)
	m.freeGuestBuffer(callCtx, mod, respPtr, respLen)
	return out, nil
}

// allocAndWrite uses the guest's allocator to reserve enough room for the
// envelope and copies the bytes in. It picks y_extension_malloc when
// present, otherwise falls back to TinyGo's exported "malloc".
func (m *activeManager) allocAndWrite(ctx context.Context, mod *loadedModule, data []byte) (uint32, uint32, error) {
	alloc := mod.fnMalloc
	if alloc == nil {
		alloc = mod.module.ExportedFunction(ExportMalloc)
	}
	if alloc == nil {
		alloc = mod.module.ExportedFunction(ExportMallocAlias)
	}
	if alloc == nil {
		return 0, 0, newExtensionError(mod.info.Manifest.ID, CodeABIMismatch,
			"guest exports neither y_extension_malloc nor malloc", nil)
	}
	results, err := alloc.Call(ctx, uint64(len(data)))
	if err != nil {
		return 0, 0, classifyCallError(mod.info.Manifest.ID, err)
	}
	if len(results) == 0 {
		return 0, 0, newExtensionError(mod.info.Manifest.ID, CodeABIMismatch,
			"allocator returned no value", nil)
	}
	ptr := api.DecodeU32(results[0])
	if ptr == 0 {
		return 0, 0, newExtensionError(mod.info.Manifest.ID, CodeMemoryLimit,
			"allocator returned a null pointer", nil)
	}
	mem := mod.module.Memory()
	if mem == nil {
		return 0, 0, newExtensionError(mod.info.Manifest.ID, CodeABIMismatch,
			"guest does not export memory", nil)
	}
	if !mem.Write(ptr, data) {
		return 0, 0, newExtensionError(mod.info.Manifest.ID, CodeMemoryLimit,
			"failed to write envelope into guest memory", nil)
	}
	return ptr, uint32(len(data)), nil
}

// freeGuestBuffer best-effort release of guest memory. Errors are swallowed
// because the call already completed; logging happens at the caller's
// discretion.
func (m *activeManager) freeGuestBuffer(ctx context.Context, mod *loadedModule, ptr, length uint32) {
	if ptr == 0 || length == 0 {
		return
	}
	fn := mod.fnFree
	if fn == nil {
		fn = mod.module.ExportedFunction(ExportFree)
	}
	if fn == nil {
		return
	}
	_, _ = fn.Call(ctx, uint64(ptr), uint64(length))
}

// instantiate loads the wasm bytes, validates the ABI and registers the
// host module if needed.
func (m *activeManager) instantiate(ctx context.Context, e *entry) (*loadedModule, error) {
	bytes, err := readEntry(e.dir, e.manifest.Entry)
	if err != nil {
		return nil, err
	}

	limits := m.cfg.resolveLimits(e.manifest)
	rt, err := m.ensureRuntime(ctx, limits.MemoryPages)
	if err != nil {
		return nil, err
	}

	allowed := m.cfg.resolvedAllowed()
	grants := ResolveCapabilityGrants(e.manifest.Capabilities, allowed)

	moduleCfg := wazero.NewModuleConfig().
		WithName(e.manifest.ID).
		WithStartFunctions(). // do not auto-call _start; we drive init explicitly
		WithStdout(nil).
		WithStderr(nil).
		WithStdin(nil)

	mod, err := rt.InstantiateWithConfig(ctx, bytes, moduleCfg)
	if err != nil {
		return nil, fmt.Errorf("instantiate %s: %w", e.manifest.ID, err)
	}
	if err := verifyABI(mod); err != nil {
		_ = mod.Close(ctx)
		return nil, newExtensionError(e.manifest.ID, CodeABIMismatch, err.Error(), err)
	}

	out := &loadedModule{
		info:       e.info,
		module:     mod,
		limits:     limits,
		grants:     grants,
		execGate:   make(chan struct{}, 1),
		accepting:  true,
		drained:    make(chan struct{}),
		fnInit:     mod.ExportedFunction(ExportInit),
		fnHandle:   mod.ExportedFunction(ExportHandle),
		fnShutdown: mod.ExportedFunction(ExportShutdown),
		fnFree:     mod.ExportedFunction(ExportFree),
		fnMalloc:   mod.ExportedFunction(ExportMalloc),
	}
	if out.fnMalloc == nil {
		out.fnMalloc = mod.ExportedFunction(ExportMallocAlias)
	}
	return out, nil
}

func (m *activeManager) ensureRuntime(ctx context.Context, memoryPages uint32) (wazero.Runtime, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt := m.runtimes[memoryPages]; rt != nil {
		return rt, nil
	}
	cfg := wazero.NewRuntimeConfig().WithCloseOnContextDone(true)
	if memoryPages > 0 {
		cfg = cfg.WithMemoryLimitPages(memoryPages)
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("instantiate WASI: %w", err)
	}
	if _, err := registerHostModule(ctx, rt, m.cfg); err != nil {
		_ = rt.Close(ctx)
		return nil, err
	}
	if m.runtimes == nil {
		m.runtimes = make(map[uint32]wazero.Runtime)
	}
	m.runtimes[memoryPages] = rt
	return rt, nil
}

func (m *activeManager) removeFromOrder(id string) {
	for i, existing := range m.loadOrd {
		if existing == id {
			m.loadOrd = append(m.loadOrd[:i], m.loadOrd[i+1:]...)
			return
		}
	}
}

// classifyCallError maps wazero traps and timeout errors onto the
// structured ExtensionError contract.
func classifyCallError(id string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return newExtensionError(id, CodeTimeout, "guest call timed out", err)
	case errors.Is(err, context.Canceled):
		return newExtensionError(id, CodeTimeout, "guest call cancelled", err)
	}
	return newExtensionError(id, CodeTrap, err.Error(), err)
}

// verifyABI ensures the guest exports the minimum surface required by
// y.wasm.v1. It is intentionally lenient about the optional malloc
// alternatives because TinyGo guests only export "malloc".
func verifyABI(mod api.Module) error {
	if mod.Memory() == nil {
		return errors.New("guest must export memory")
	}
	for _, name := range []string{ExportInit, ExportHandle, ExportShutdown, ExportFree} {
		if mod.ExportedFunction(name) == nil {
			return fmt.Errorf("guest must export %q", name)
		}
	}
	if mod.ExportedFunction(ExportMalloc) == nil &&
		mod.ExportedFunction(ExportMallocAlias) == nil {
		return fmt.Errorf("guest must export %q or %q", ExportMalloc, ExportMallocAlias)
	}
	if v := mod.ExportedFunction(ExportABIVersion); v != nil {
		results, err := v.Call(context.Background())
		if err != nil {
			return fmt.Errorf("call %s: %w", ExportABIVersion, err)
		}
		if len(results) > 0 {
			got := api.DecodeU32(results[0])
			if got != SupportedABIVersion {
				return fmt.Errorf("ABI version %d != supported %d", got, SupportedABIVersion)
			}
		}
	}
	return nil
}

// callScope tracks per-call accounting. It is stored in the call context
// so host functions can read it without locking.
type callScope struct {
	module *loadedModule
	cfg    Config

	logBytesUsed atomic.Uint64
	hostCalls    atomic.Uint64
}

func newCallScope(mod *loadedModule, cfg Config) *callScope {
	return &callScope{module: mod, cfg: cfg}
}

// reserveHostCall returns true when the receiver should reject another host call.
func (s *callScope) reserveHostCall() error {
	if s == nil || s.module == nil {
		return errors.New("missing call scope")
	}
	max := uint64(s.module.limits.MaxHostCalls)
	if max == 0 {
		s.hostCalls.Add(1)
		return nil
	}
	if s.hostCalls.Add(1) > max {
		return newExtensionError(s.module.info.Manifest.ID, CodeHostCallQuota,
			fmt.Sprintf("host call quota %d exceeded", max), nil)
	}
	return nil
}

// reserveLogBytes accounts for log output. It returns an error when the
// remaining budget is exhausted; partial messages are still recorded with
// truncated bodies because the host may want to surface "log truncated".
func (s *callScope) reserveLogBytes(n uint32) (uint32, error) {
	if s == nil || s.module == nil {
		return 0, errors.New("missing call scope")
	}
	max := uint64(s.module.limits.MaxLogBytes)
	if max == 0 {
		s.logBytesUsed.Add(uint64(n))
		return n, nil
	}
	used := s.logBytesUsed.Load()
	if used >= max {
		return 0, newExtensionError(s.module.info.Manifest.ID, CodeLogQuotaExceeded,
			"log quota exhausted", nil)
	}
	allow := max - used
	if uint64(n) > allow {
		n = uint32(allow)
	}
	s.logBytesUsed.Add(uint64(n))
	return n, nil
}

// callContextKey indexes callScope in context.Context.
type callContextKey struct{}

// activeCallScope retrieves the scope attached to the supplied context, if
// any. Used by host functions invoked through the guest.
func activeCallScope(ctx context.Context) *callScope {
	if ctx == nil {
		return nil
	}
	v := ctx.Value(callContextKey{})
	if v == nil {
		return nil
	}
	scope, _ := v.(*callScope)
	return scope
}

// timestampMS returns wall-clock time in milliseconds. Exposed so tests
// can stub it deterministically.
var timestampMS = func() int64 {
	return time.Now().UnixMilli()
}
