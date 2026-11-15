//go:build feature_wasm_ext

package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diasYuri/y/pkg/extensions"
	"github.com/diasYuri/y/pkg/tools"
)

func componentTestManager(t *testing.T, manifest string, module []byte) *activeManager {
	t.Helper()
	root := t.TempDir()
	dir := writeExtension(t, root, "guest", manifest)
	if err := os.WriteFile(filepath.Join(dir, "module.wasm"), module, 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(Config{ExtensionDirs: []string{root}}).(*activeManager)
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := m.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWASMComponent_SchemasAndScopedUnload(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		wantError    bool
	}{
		{"valid", `{"type":"object","properties":{"content":{"type":"array"}},"required":["content"]}`, false},
		{"invalid output", `{"type":"object","required":["missing"]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := validManifest + "\n[[tools]]\nname = \"search\"\ninput_schema = " + quoteSchema(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`) + "\noutput_schema = " + quoteSchema(tc.output) + "\n"
			m := componentTestManager(t, manifest, minimalWASM)
			host := extensions.NewHost(tools.NewRegistry())
			defer host.Close(context.Background())
			defs, err := ComponentDefinitions(m, host, "fake.search")
			if err != nil {
				t.Fatal(err)
			}
			if err := host.AddDefinitions(context.Background(), defs); err != nil {
				t.Fatal(err)
			}
			if _, err := host.Registry.Handle(context.Background(), tools.ToolRequest{Name: "wasm_fake_search_search", Arguments: json.RawMessage(`{}`)}); err == nil {
				t.Fatal("missing required input accepted")
			}
			response, err := host.Registry.Handle(context.Background(), tools.ToolRequest{Name: "wasm_fake_search_search", Arguments: json.RawMessage(`{"q":"test"}`)})
			if (err != nil) != tc.wantError {
				t.Fatalf("response=%+v error=%v", response, err)
			}
			if !tc.wantError && (len(response.Content) != 1 || response.Content[0].Text != "ok") {
				t.Fatalf("response=%+v", response)
			}
			if err := host.Uninstall(context.Background(), "wasm.fake.search"); err != nil {
				t.Fatal(err)
			}
			if len(host.Registry.List()) != 0 {
				t.Fatal("tools survived unload")
			}
			if _, err := host.Registry.Handle(context.Background(), tools.ToolRequest{Name: "wasm_fake_search_search"}); !errors.Is(err, tools.ErrToolNotFound) {
				t.Fatalf("call after unload: %v", err)
			}
			info, _ := m.Get("fake.search")
			if info.Status != StatusDiscovered {
				t.Fatalf("status=%s", info.Status)
			}
		})
	}
}

func quoteSchema(value string) string { encoded, _ := json.Marshal(value); return string(encoded) }

func TestWASMComponent_FailedRegistrationRollsBackModuleAndTools(t *testing.T) {
	m := componentTestManager(t, validManifest+"\n[[tools]]\nname = \"first\"\n[[tools]]\nname = \"conflict\"\n", minimalWASM)
	host := extensions.NewHost(tools.NewRegistry())
	defer host.Close(context.Background())
	// A legacy registration belongs to a different owner and must survive rollback.
	if err := host.Registry.Add(tools.ToolDescriptor{Name: "wasm_fake_search_conflict"}, tools.ToolHandlerFunc(func(context.Context, tools.ToolRequest) (tools.ToolResponse, error) { return tools.ToolResponse{}, nil })); err != nil {
		t.Fatal(err)
	}
	defs, err := ComponentDefinitions(m, host, "fake.search")
	if err != nil {
		t.Fatal(err)
	}
	if err := host.AddDefinitions(context.Background(), defs); err == nil {
		t.Fatal("expected collision")
	}
	if got := host.Registry.List(); len(got) != 1 || got[0].Name != "wasm_fake_search_conflict" {
		t.Fatalf("tools=%v", got)
	}
	info, _ := m.Get("fake.search")
	if info.Status != StatusDiscovered {
		t.Fatalf("module survived rollback: %s", info.Status)
	}
}

func TestEnabledManager_RetiringRejectsCallsAndCanResumeUnload(t *testing.T) {
	m := componentTestManager(t, validManifest, minimalWASM)
	ctx := context.Background()
	if err := m.Load(ctx, "fake.search"); err != nil {
		t.Fatal(err)
	}
	mod := m.loaded["fake.search"]
	if !mod.acquireCall() {
		t.Fatal("could not admit call")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := m.Unload(canceled, "fake.search"); !errors.Is(err, context.Canceled) {
		t.Fatalf("unload=%v", err)
	}
	if _, err := m.CallTool(ctx, "fake.search", ToolRequest{Tool: "search"}); err == nil || !strings.Contains(err.Error(), "retiring") {
		t.Fatalf("call during drain=%v", err)
	}
	mod.releaseCall()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Unload(ctx, "fake.search"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if !mod.module.IsClosed() {
		t.Fatal("module remained open")
	}
	if err := m.Load(ctx, "fake.search"); err != nil {
		t.Fatalf("reload after drain: %v", err)
	}
}

func TestEnabledManager_MemoryCeilingsIndependentOfLoadOrder(t *testing.T) {
	m := componentTestManager(t, validManifest, minimalWASM)
	ctx := context.Background()
	for _, pages := range []uint32{16, 2} {
		rt, err := m.ensureRuntime(ctx, pages)
		if err != nil {
			t.Fatal(err)
		}
		mod, err := rt.Instantiate(ctx, minimalWASM)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := mod.Memory().Grow(pages - 2); !ok {
			t.Fatal("could not grow to ceiling")
		}
		if _, ok := mod.Memory().Grow(1); ok {
			t.Fatalf("exceeded %d page ceiling", pages)
		}
	}
}

func TestWASMComponent_RuntimeViewPinsModuleUntilRelease(t *testing.T) {
	m := componentTestManager(t, validManifest+"\n[[tools]]\nname = \"search\"\n", minimalWASM)
	host := extensions.NewHost(tools.NewRegistry())
	defer host.Close(context.Background())
	ctx := context.Background()
	defs, err := ComponentDefinitions(m, host, "fake.search")
	if err != nil {
		t.Fatal(err)
	}
	if err := host.AddDefinitions(ctx, defs); err != nil {
		t.Fatal(err)
	}
	view, err := host.AcquireRuntimeView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	info := view.Info()
	if len(info.Tools) != 1 || info.Tools[0].Owner != "extensions/wasm.fake.search" || info.Tools[0].Generation == 0 {
		t.Fatalf("origins=%+v", info.Tools)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := host.Uninstall(canceled, "wasm.fake.search"); !errors.Is(err, context.Canceled) {
		t.Fatalf("uninstall=%v", err)
	}
	// A canceled reconcile does not commit. Retry with a bounded wait while the
	// leased view holds the original generation alive.
	deadline, stop := context.WithTimeout(ctx, 10*time.Millisecond)
	defer stop()
	if err := host.Uninstall(deadline, "wasm.fake.search"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("uninstall while leased=%v", err)
	}
	if len(host.Registry.List()) != 0 {
		t.Fatal("retired tools still advertised")
	}
	if _, err := view.Tools().Handle(ctx, tools.ToolRequest{Name: "wasm_fake_search_search", Arguments: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("leased tool=%v", err)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if err := host.Close(ctx); err != nil {
		t.Fatal(err)
	}
	module, _ := m.Get("fake.search")
	if module.Status != StatusDiscovered {
		t.Fatalf("module not unloaded: %s", module.Status)
	}
}

func TestWASMComponent_FailedInitLeavesNoRegistrations(t *testing.T) {
	m := componentTestManager(t, validManifest+"\n[[tools]]\nname = \"search\"\n", buildABIToolModule(mustMarshalResponse(Response{OK: false})))
	host := extensions.NewHost(tools.NewRegistry())
	defer host.Close(context.Background())
	defs, err := ComponentDefinitions(m, host, "fake.search")
	if err != nil {
		t.Fatal(err)
	}
	if err := host.AddDefinitions(context.Background(), defs); err == nil {
		t.Fatal("failed init accepted")
	}
	if len(host.Registry.List()) != 0 || len(m.loaded) != 0 {
		t.Fatal("failed init leaked registrations or module")
	}
}

func TestEnabledManager_LegacyLimitsArePreserved(t *testing.T) {
	m := NewManager(Config{DefaultMemoryPages: 8, DefaultTimeoutMS: 23}).(*activeManager)
	defer m.Close(context.Background())
	limits := m.cfg.resolveLimits(Manifest{})
	if limits.MemoryPages != 8 || limits.TimeoutMS != 23 {
		t.Fatalf("limits=%+v", limits)
	}
}

func TestEnabledManager_CanceledQueuedCallReturnsWithoutWaiting(t *testing.T) {
	m := componentTestManager(t, validManifest, minimalWASM)
	if err := m.Load(context.Background(), "fake.search"); err != nil {
		t.Fatal(err)
	}
	mod := m.loaded["fake.search"]
	mod.execGate <- struct{}{}
	defer func() { <-mod.execGate }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { _, err := m.CallTool(ctx, "fake.search", ToolRequest{Tool: "search"}); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled call waited for guest execution slot")
	}
}

func TestEnabledManager_CloseUpdatesModuleStatus(t *testing.T) {
	m := componentTestManager(t, validManifest, minimalWASM)
	if err := m.Load(context.Background(), "fake.search"); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	info, err := m.Get("fake.search")
	if err != nil || info.Status != StatusDiscovered {
		t.Fatalf("info=%+v error=%v", info, err)
	}
}
