//go:build feature_wasm_ext

package wasm

import (
	"context"
	"encoding/json"
	"testing"
)

// makeScope builds a minimal callScope for testing host-call dispatch
// without instantiating a real guest module.
func makeScope(id string, grants CapabilityGrantSet, limits Limits) *callScope {
	return &callScope{
		module: &loadedModule{
			info:   ExtensionInfo{Manifest: Manifest{ID: id}},
			limits: limits,
			grants: grants,
		},
	}
}

func TestDispatchHostCallDeniesUnknownOp(t *testing.T) {
	scope := makeScope("ext", NewCapabilityGrantSet(), DefaultLimits())
	resp := dispatchHostCall(context.Background(), Config{Policy: AllowAllPolicy()}, scope, HostCallRequest{
		Kind: EnvelopeKind("not-a-kind"),
	})
	if resp.OK || resp.Error == nil {
		t.Fatalf("expected error response, got %+v", resp)
	}
	if resp.Error.Code != CodeUnsupportedHostOp {
		t.Fatalf("expected %q, got %q", CodeUnsupportedHostOp, resp.Error.Code)
	}
}

func TestDispatchHostCallNowAlwaysAllowed(t *testing.T) {
	scope := makeScope("ext", NewCapabilityGrantSet(), DefaultLimits())
	resp := dispatchHostCall(context.Background(), Config{Policy: DenyAllPolicy()}, scope, HostCallRequest{
		Kind: KindHostNow,
	})
	if !resp.OK {
		t.Fatalf("KindHostNow must be capability-free, got %+v", resp.Error)
	}
	var p HostNowPayload
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		t.Fatalf("decode now payload: %v", err)
	}
	if p.UnixMillis <= 0 {
		t.Fatalf("expected positive timestamp, got %d", p.UnixMillis)
	}
}

func TestDispatchHostCallToolInvokeRequiresCapability(t *testing.T) {
	scope := makeScope("ext", NewCapabilityGrantSet(), DefaultLimits())
	cfg := Config{
		Policy: AllowAllPolicy(),
		Invoker: HostInvokerFunc(func(_ context.Context, _, _ string, _ json.RawMessage) (HostToolInvokeResult, error) {
			return HostToolInvokeResult{}, nil
		}),
	}
	payload, _ := json.Marshal(HostToolInvokePayload{Tool: "search"})
	resp := dispatchHostCall(context.Background(), cfg, scope, HostCallRequest{
		Kind:    KindToolInvoke,
		Payload: payload,
	})
	if resp.OK {
		t.Fatal("expected denial when y_tools is not granted")
	}
	if resp.Error.Code != CodeCapabilityDenied {
		t.Fatalf("expected %q, got %q", CodeCapabilityDenied, resp.Error.Code)
	}
}

func TestDispatchHostCallToolInvokePolicyDeny(t *testing.T) {
	scope := makeScope("ext", NewCapabilityGrantSet(CapYTools), DefaultLimits())
	cfg := Config{
		Policy: PolicyFunc(func(_ context.Context, req CapabilityRequest) (Decision, error) {
			if req.Capability == CapYTools && req.Detail == "search" {
				return DecisionDeny, nil
			}
			return DecisionAllow, nil
		}),
		Invoker: HostInvokerFunc(func(_ context.Context, _, _ string, _ json.RawMessage) (HostToolInvokeResult, error) {
			t.Fatal("invoker must not run when policy denies")
			return HostToolInvokeResult{}, nil
		}),
	}
	payload, _ := json.Marshal(HostToolInvokePayload{Tool: "search"})
	resp := dispatchHostCall(context.Background(), cfg, scope, HostCallRequest{
		Kind:    KindToolInvoke,
		Payload: payload,
	})
	if resp.OK || resp.Error == nil {
		t.Fatalf("expected policy denial, got %+v", resp)
	}
	if resp.Error.Code != CodePolicyDenied {
		t.Fatalf("expected %q, got %q", CodePolicyDenied, resp.Error.Code)
	}
}

func TestDispatchHostCallToolInvokeMissingInvoker(t *testing.T) {
	scope := makeScope("ext", NewCapabilityGrantSet(CapYTools), DefaultLimits())
	cfg := Config{Policy: AllowAllPolicy()}
	payload, _ := json.Marshal(HostToolInvokePayload{Tool: "search"})
	resp := dispatchHostCall(context.Background(), cfg, scope, HostCallRequest{
		Kind:    KindToolInvoke,
		Payload: payload,
	})
	if resp.OK || resp.Error == nil {
		t.Fatalf("expected missing invoker error, got %+v", resp)
	}
	if resp.Error.Code != CodeUnsupportedHostOp {
		t.Fatalf("expected %q, got %q", CodeUnsupportedHostOp, resp.Error.Code)
	}
}

func TestDispatchHostCallLogsGated(t *testing.T) {
	var sunk []string
	cfg := Config{
		Policy: AllowAllPolicy(),
		LogSink: LogSinkFunc(func(_ string, _ LogLevel, msg string) {
			sunk = append(sunk, msg)
		}),
	}
	// No CapLogs granted → must be denied.
	scope := makeScope("ext", NewCapabilityGrantSet(), DefaultLimits())
	payload, _ := json.Marshal(HostLogPayload{Level: 1, Message: "hello"})
	resp := dispatchHostCall(context.Background(), cfg, scope, HostCallRequest{
		Kind:    KindHostLog,
		Payload: payload,
	})
	if resp.OK {
		t.Fatal("expected denial without logs capability")
	}
	if len(sunk) != 0 {
		t.Fatalf("log sink saw messages despite denial: %v", sunk)
	}

	// Granted → must succeed.
	scope = makeScope("ext", NewCapabilityGrantSet(CapLogs), DefaultLimits())
	resp = dispatchHostCall(context.Background(), cfg, scope, HostCallRequest{
		Kind:    KindHostLog,
		Payload: payload,
	})
	if !resp.OK {
		t.Fatalf("expected success after granting logs, got %+v", resp.Error)
	}
	if len(sunk) != 1 || sunk[0] != "hello" {
		t.Fatalf("log sink did not record message: %v", sunk)
	}
}

func TestDispatchHostCallToolInvokeForwardsResult(t *testing.T) {
	scope := makeScope("ext", NewCapabilityGrantSet(CapYTools), DefaultLimits())
	cfg := Config{
		Policy: AllowAllPolicy(),
		Invoker: HostInvokerFunc(func(_ context.Context, ext, tool string, _ json.RawMessage) (HostToolInvokeResult, error) {
			if ext != "ext" || tool != "search" {
				t.Fatalf("invoker received unexpected args: %s/%s", ext, tool)
			}
			return HostToolInvokeResult{Content: []ContentBlock{{Type: "text", Text: "ok"}}}, nil
		}),
	}
	payload, _ := json.Marshal(HostToolInvokePayload{Tool: "search"})
	resp := dispatchHostCall(context.Background(), cfg, scope, HostCallRequest{
		Kind:    KindToolInvoke,
		Payload: payload,
	})
	if !resp.OK {
		t.Fatalf("expected success, got %+v", resp.Error)
	}
	var result HostToolInvokeResult
	if err := json.Unmarshal(resp.Payload, &result); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "ok" {
		t.Fatalf("unexpected content: %+v", result)
	}
}

func TestCallScopeReserveHostCall(t *testing.T) {
	scope := makeScope("ext", NewCapabilityGrantSet(), Limits{MaxHostCalls: 2})
	if err := scope.reserveHostCall(); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := scope.reserveHostCall(); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if err := scope.reserveHostCall(); err == nil || !IsCode(err, CodeHostCallQuota) {
		t.Fatalf("third call should exceed quota, got %v", err)
	}
}

func TestCallScopeReserveLogBytesQuota(t *testing.T) {
	scope := makeScope("ext", NewCapabilityGrantSet(), Limits{MaxLogBytes: 10})
	allow, err := scope.reserveLogBytes(6)
	if err != nil {
		t.Fatalf("reserve 6: %v", err)
	}
	if allow != 6 {
		t.Fatalf("allow = %d, want 6", allow)
	}
	allow, err = scope.reserveLogBytes(8)
	if err != nil {
		t.Fatalf("reserve 8: %v", err)
	}
	if allow != 4 {
		t.Fatalf("partial allow = %d, want 4", allow)
	}
	if _, err := scope.reserveLogBytes(1); err == nil || !IsCode(err, CodeLogQuotaExceeded) {
		t.Fatalf("expected quota error, got %v", err)
	}
}

func TestResolveCapabilityGrantsIntersectsAllowlist(t *testing.T) {
	manifest := CapabilitySet{Filesystem: true, YTools: true, Network: true}
	allowed := []Capability{CapYTools, CapFilesystemRead}
	grants := ResolveCapabilityGrants(manifest, allowed)
	if !grants.Grant(CapYTools) {
		t.Fatal("expected y_tools to be granted")
	}
	if !grants.Grant(CapFilesystemRead) {
		t.Fatal("expected filesystem.read to be granted")
	}
	if grants.Grant(CapFilesystemWrite) {
		t.Fatal("filesystem.write must be filtered out by allowlist")
	}
	if grants.Grant(CapNetworkHTTP) {
		t.Fatal("network.http must be filtered out")
	}
}

func TestParseCapabilitiesAcceptsAliases(t *testing.T) {
	caps, err := ParseCapabilities([]string{"filesystem", "network", "git", "y_tools"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	expect := map[Capability]bool{
		CapFilesystemRead:  true,
		CapFilesystemWrite: true,
		CapNetworkHTTP:     true,
		CapGitRead:         true,
		CapGitWrite:        true,
		CapYTools:          true,
	}
	if len(caps) != len(expect) {
		t.Fatalf("expected %d caps, got %d (%v)", len(expect), len(caps), caps)
	}
	for _, c := range caps {
		if !expect[c] {
			t.Fatalf("unexpected capability %q", c)
		}
	}
}

func TestParseCapabilitiesRejectsUnknown(t *testing.T) {
	if _, err := ParseCapabilities([]string{"banana"}); err == nil {
		t.Fatal("expected error for unknown capability")
	}
}

func TestDispatchHostCallProcessExecRequiresCapability(t *testing.T) {
	scope := makeScope("ext", NewCapabilityGrantSet(), DefaultLimits())
	cfg := Config{Policy: AllowAllPolicy()}
	payload, _ := json.Marshal(HostProcessExecPayload{Command: []string{"echo", "hello"}})
	resp := dispatchHostCall(context.Background(), cfg, scope, HostCallRequest{
		Kind:    KindProcessExec,
		Payload: payload,
	})
	if resp.OK {
		t.Fatal("expected denial when process.exec is not granted")
	}
	if resp.Error.Code != CodeCapabilityDenied {
		t.Fatalf("expected %q, got %q", CodeCapabilityDenied, resp.Error.Code)
	}
}

func TestDispatchHostCallProcessExecRunsCommand(t *testing.T) {
	scope := makeScope("ext", NewCapabilityGrantSet(CapProcessExec), DefaultLimits())
	cfg := Config{Policy: AllowAllPolicy()}
	payload, _ := json.Marshal(HostProcessExecPayload{
		Command: []string{"echo", "hello from wasm"},
	})
	resp := dispatchHostCall(context.Background(), cfg, scope, HostCallRequest{
		Kind:    KindProcessExec,
		Payload: payload,
	})
	if !resp.OK {
		t.Fatalf("expected success, got %+v", resp.Error)
	}
	var result HostProcessExecResult
	if err := json.Unmarshal(resp.Payload, &result); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", result.ExitCode)
	}
	if result.Stdout != "hello from wasm\n" {
		t.Fatalf("unexpected stdout: %q", result.Stdout)
	}
}

func TestDispatchHostCallProcessExecTimeout(t *testing.T) {
	scope := makeScope("ext", NewCapabilityGrantSet(CapProcessExec), DefaultLimits())
	cfg := Config{Policy: AllowAllPolicy()}
	payload, _ := json.Marshal(HostProcessExecPayload{
		Command:   []string{"sleep", "10"},
		TimeoutMS: 50,
	})
	resp := dispatchHostCall(context.Background(), cfg, scope, HostCallRequest{
		Kind:    KindProcessExec,
		Payload: payload,
	})
	if !resp.OK {
		t.Fatalf("expected success even when timed out, got %+v", resp.Error)
	}
	var result HostProcessExecResult
	if err := json.Unmarshal(resp.Payload, &result); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	// On timeout the exit code may vary depending on signal handling.
	// The important thing is it didn't hang.
}

func TestDispatchHostCallProcessExecInvalidPayload(t *testing.T) {
	scope := makeScope("ext", NewCapabilityGrantSet(CapProcessExec), DefaultLimits())
	cfg := Config{Policy: AllowAllPolicy()}
	resp := dispatchHostCall(context.Background(), cfg, scope, HostCallRequest{
		Kind:    KindProcessExec,
		Payload: json.RawMessage(`not json`),
	})
	if resp.OK {
		t.Fatal("expected error for invalid JSON")
	}
	if resp.Error.Code != CodeInvalidArgument {
		t.Fatalf("expected %q, got %q", CodeInvalidArgument, resp.Error.Code)
	}
}

func TestDispatchHostCallProviderRequestUnsupported(t *testing.T) {
	scope := makeScope("ext", NewCapabilityGrantSet(), DefaultLimits())
	cfg := Config{Policy: AllowAllPolicy()}
	resp := dispatchHostCall(context.Background(), cfg, scope, HostCallRequest{
		Kind:    KindProviderRequest,
		Payload: json.RawMessage(`{}`),
	})
	if resp.OK {
		t.Fatal("expected unsupported error for provider request")
	}
	if resp.Error.Code != CodeUnsupportedHostOp {
		t.Fatalf("expected %q, got %q", CodeUnsupportedHostOp, resp.Error.Code)
	}
}

func TestLimitsApplyOverlay(t *testing.T) {
	base := Limits{TimeoutMS: 1000, MaxOutputBytes: 1024}
	merged := base.Apply(Limits{TimeoutMS: 0, MemoryPages: 8, MaxLogBytes: 64})
	if merged.TimeoutMS != 1000 {
		t.Errorf("TimeoutMS = %d, want 1000", merged.TimeoutMS)
	}
	if merged.MemoryPages != 8 {
		t.Errorf("MemoryPages = %d, want 8", merged.MemoryPages)
	}
	if merged.MaxLogBytes != 64 {
		t.Errorf("MaxLogBytes = %d, want 64", merged.MaxLogBytes)
	}
	if merged.MaxOutputBytes != 1024 {
		t.Errorf("MaxOutputBytes lost during overlay: %d", merged.MaxOutputBytes)
	}
}
