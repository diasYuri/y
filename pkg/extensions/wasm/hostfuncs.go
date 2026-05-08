//go:build feature_wasm_ext

package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// registerHostModule installs the pi_host module and exports its functions.
// The host functions are intentionally tiny: they read JSON envelopes from
// guest memory, dispatch to native helpers, and write JSON results back via
// the guest allocator. Capability checks happen here so a misconfigured
// guest cannot bypass policy by skipping a layer.
func registerHostModule(ctx context.Context, rt wazero.Runtime, cfg Config) (api.Module, error) {
	builder := rt.NewHostModuleBuilder(HostModuleName)

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(makeHostCall(cfg)),
			[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI64}).
		WithParameterNames("req_ptr", "req_len").
		WithResultNames("packed").
		Export(FuncHostCall)

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(makeHostLog(cfg)),
			[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			nil).
		WithParameterNames("level", "msg_ptr", "msg_len").
		Export(FuncHostLog)

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(hostNow),
			nil,
			[]api.ValueType{api.ValueTypeI64}).
		WithResultNames("unix_millis").
		Export(FuncHostNow)

	mod, err := builder.Instantiate(ctx)
	if err != nil {
		return nil, fmt.Errorf("instantiate %s: %w", HostModuleName, err)
	}
	return mod, nil
}

// makeHostCall returns a wazero host function that handles every kind that
// flows through pi_host_call.
func makeHostCall(cfg Config) func(ctx context.Context, mod api.Module, stack []uint64) {
	return func(ctx context.Context, mod api.Module, stack []uint64) {
		defer func() {
			if r := recover(); r != nil {
				stack[0] = 0 // signal failure with a zero pointer
			}
		}()

		ptr := api.DecodeU32(stack[0])
		length := api.DecodeU32(stack[1])
		stack[0] = 0

		scope := activeCallScope(ctx)
		if scope == nil {
			writeFabricatedError(mod, &stack[0], "internal", "no active call scope", nil)
			return
		}
		if err := scope.reserveHostCall(); err != nil {
			writeFabricatedError(mod, &stack[0], CodeHostCallQuota, err.Error(), nil)
			return
		}

		mem := mod.Memory()
		if mem == nil {
			writeFabricatedError(mod, &stack[0], CodeABIMismatch, "host: missing memory", nil)
			return
		}
		buf, ok := mem.Read(ptr, length)
		if !ok {
			writeFabricatedError(mod, &stack[0], CodeABIMismatch,
				"host: invalid request pointer", nil)
			return
		}
		var req HostCallRequest
		if err := json.Unmarshal(buf, &req); err != nil {
			writeFabricatedError(mod, &stack[0], CodeInvalidArgument,
				"host: invalid request JSON", err)
			return
		}

		resp := dispatchHostCall(ctx, cfg, scope, req)
		respBytes, err := MarshalResponse(resp)
		if err != nil {
			writeFabricatedError(mod, &stack[0], CodeInternal,
				"host: marshal response", err)
			return
		}
		if scope.module.limits.MaxOutputBytes > 0 && uint32(len(respBytes)) > scope.module.limits.MaxOutputBytes {
			writeFabricatedError(mod, &stack[0], CodeOutputTooLarge,
				"host: response exceeds output limit", nil)
			return
		}
		newPtr, ok := writeResponseToGuest(ctx, mod, respBytes)
		if !ok {
			writeFabricatedError(mod, &stack[0], CodeMemoryLimit,
				"host: write response failed", nil)
			return
		}
		stack[0] = PackPointerLength(newPtr, uint32(len(respBytes)))
	}
}

// makeHostLog drains log messages, enforcing the per-call quota. Logs are
// silently truncated when the quota is exhausted because returning an error
// is impossible from a void host function.
func makeHostLog(cfg Config) func(ctx context.Context, mod api.Module, stack []uint64) {
	return func(ctx context.Context, mod api.Module, stack []uint64) {
		defer func() {
			_ = recover()
		}()

		level := LogLevel(api.DecodeU32(stack[0]))
		ptr := api.DecodeU32(stack[1])
		length := api.DecodeU32(stack[2])

		scope := activeCallScope(ctx)
		if scope == nil {
			return
		}
		// Logs are gated by the logs capability so policy can revoke them
		// even if the manifest declared them.
		if !scope.module.grants.Grant(CapLogs) {
			return
		}
		policy := cfg.resolvedPolicy()
		decision, err := policy.Authorize(ctx, CapabilityRequest{
			ExtensionID: scope.module.info.Manifest.ID,
			Capability:  CapLogs,
			Kind:        KindHostLog,
		})
		if err != nil || decision != DecisionAllow {
			return
		}
		allow, err := scope.reserveLogBytes(length)
		if err != nil || allow == 0 {
			return
		}
		mem := mod.Memory()
		if mem == nil {
			return
		}
		raw, ok := mem.Read(ptr, allow)
		if !ok {
			return
		}
		msg := string(raw)
		if cfg.LogSink != nil {
			cfg.LogSink.Log(scope.module.info.Manifest.ID, level, msg)
		}
	}
}

// hostNow is a leaf function: it takes no parameters and returns the wall
// clock millis. Useful for guests that need a coarse "wall time" without
// requesting any capabilities.
func hostNow(_ context.Context, _ api.Module, stack []uint64) {
	stack[0] = uint64(timestampMS())
}

// dispatchHostCall services every kind of pi_host_call envelope. Each
// branch is responsible for capability gating and structured error
// reporting.
func dispatchHostCall(ctx context.Context, cfg Config, scope *callScope, req HostCallRequest) Response {
	id := scope.module.info.Manifest.ID
	switch req.Kind {
	case KindHostNow:
		return successResponse(HostNowPayload{UnixMillis: timestampMS()})

	case KindHostLog:
		var p HostLogPayload
		if err := json.Unmarshal(req.Payload, &p); err != nil {
			return errorResponse(CodeInvalidArgument, "log payload invalid", false)
		}
		if !scope.module.grants.Grant(CapLogs) {
			return errorResponse(CodeCapabilityDenied,
				fmt.Sprintf("capability %q not granted", CapLogs), false)
		}
		policy := cfg.resolvedPolicy()
		dec, err := policy.Authorize(ctx, CapabilityRequest{
			ExtensionID: id,
			Capability:  CapLogs,
			Kind:        KindHostLog,
		})
		if err != nil {
			return errorResponse(CodeInternal, err.Error(), true)
		}
		if dec != DecisionAllow {
			return errorResponse(CodePolicyDenied, "policy denied logs", false)
		}
		if cfg.LogSink != nil {
			cfg.LogSink.Log(id, LogLevel(p.Level), p.Message)
		}
		return successResponse(struct{}{})

	case KindHostCapInfo:
		return successResponse(CapabilityInfoPayload{Granted: scope.module.grants.Strings()})

	case KindToolInvoke:
		return dispatchToolInvoke(ctx, cfg, scope, req.Payload)

	case KindProcessExec:
		return dispatchProcessExec(ctx, cfg, scope, req.Payload)

	case KindProviderRequest:
		return dispatchProviderRequest(ctx, cfg, scope, req.Payload)

	default:
		return errorResponse(CodeUnsupportedHostOp,
			fmt.Sprintf("unknown host op %q", req.Kind), false)
	}
}

// dispatchToolInvoke implements the y_tools capability bridge. It honours
// both the manifest-level grant and the runtime policy.
func dispatchToolInvoke(ctx context.Context, cfg Config, scope *callScope, payload json.RawMessage) Response {
	id := scope.module.info.Manifest.ID
	if !scope.module.grants.Grant(CapYTools) {
		return errorResponse(CodeCapabilityDenied,
			fmt.Sprintf("capability %q not granted", CapYTools), false)
	}
	policy := cfg.resolvedPolicy()
	if cfg.Invoker == nil {
		return errorResponse(CodeUnsupportedHostOp,
			"y_tools invoker is not configured", false)
	}
	var body HostToolInvokePayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return errorResponse(CodeInvalidArgument, "invalid tool_invoke payload", false)
	}
	if body.Tool == "" {
		return errorResponse(CodeInvalidArgument, "missing tool name", false)
	}
	dec, err := policy.Authorize(ctx, CapabilityRequest{
		ExtensionID: id,
		Capability:  CapYTools,
		Kind:        KindToolInvoke,
		Detail:      body.Tool,
	})
	if err != nil {
		return errorResponse(CodeInternal, err.Error(), true)
	}
	if dec != DecisionAllow {
		return errorResponse(CodePolicyDenied,
			fmt.Sprintf("policy denied tool %q", body.Tool), false)
	}
	res, err := cfg.Invoker.InvokeTool(ctx, id, body.Tool, body.Arguments)
	if err != nil {
		var ee *ExtensionError
		if errors.As(err, &ee) {
			return errorResponse(ee.Code, ee.Message, ee.Retryable)
		}
		return errorResponse(CodeInternal, err.Error(), false)
	}
	return successResponse(res)
}

// dispatchProcessExec implements the process.exec capability bridge. It uses
// os/exec directly (no shell unless explicitly requested) and enforces
// timeout and output limits.
func dispatchProcessExec(ctx context.Context, cfg Config, scope *callScope, payload json.RawMessage) Response {
	id := scope.module.info.Manifest.ID
	if !scope.module.grants.Grant(CapProcessExec) {
		return errorResponse(CodeCapabilityDenied,
			fmt.Sprintf("capability %q not granted", CapProcessExec), false)
	}
	policy := cfg.resolvedPolicy()
	dec, err := policy.Authorize(ctx, CapabilityRequest{
		ExtensionID: id,
		Capability:  CapProcessExec,
		Kind:        KindProcessExec,
	})
	if err != nil {
		return errorResponse(CodeInternal, err.Error(), true)
	}
	if dec != DecisionAllow {
		return errorResponse(CodePolicyDenied, "policy denied process.exec", false)
	}

	var body HostProcessExecPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return errorResponse(CodeInvalidArgument, "invalid process_exec payload", false)
	}
	if len(body.Command) == 0 || body.Command[0] == "" {
		return errorResponse(CodeInvalidArgument, "missing command", false)
	}

	timeout := time.Duration(body.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	maxOutput := int64(body.MaxOutputBytes)
	if maxOutput <= 0 {
		maxOutput = 65536
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, body.Command[0], body.Command[1:]...)
	if body.WorkingDirectory != "" {
		cmd.Dir = body.WorkingDirectory
	}
	if len(body.Env) > 0 {
		env := make([]string, 0, len(body.Env))
		for k, v := range body.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = env
	}

	stdout, err := cmd.Output()
	exitCode := 0
	timedOut := execCtx.Err() == context.DeadlineExceeded
	var stderrStr string
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
			stderrStr = string(exitErr.Stderr)
		} else {
			return errorResponse(CodeInternal, fmt.Sprintf("exec failed: %v", err), false)
		}
	}
	stdoutStr := string(stdout)
	truncated := false
	if int64(len(stdoutStr)) > maxOutput {
		stdoutStr = stdoutStr[:maxOutput]
		truncated = true
	}
	if int64(len(stderrStr)) > maxOutput {
		stderrStr = stderrStr[:maxOutput]
		truncated = true
	}

	return successResponse(HostProcessExecResult{
		ExitCode:  exitCode,
		Stdout:    stdoutStr,
		Stderr:    stderrStr,
		Truncated: truncated,
		TimedOut:  timedOut,
	})
}

// dispatchProviderRequest handles provider calls from WASM guests.
// V2: currently returns unsupported since provider semantics across
// host-guest boundaries are deferred.
func dispatchProviderRequest(_ context.Context, _ Config, scope *callScope, _ json.RawMessage) Response {
	return errorResponse(CodeUnsupportedHostOp,
		fmt.Sprintf("provider requests are not supported in this ABI version (guest: %s)", scope.module.info.Manifest.ID), false)
}

// successResponse builds an OK response wrapping the supplied payload.
func successResponse(v any) Response {
	body, err := json.Marshal(v)
	if err != nil {
		return errorResponse(CodeInternal, err.Error(), false)
	}
	return Response{OK: true, Payload: body}
}

// errorResponse builds a structured failure response.
func errorResponse(code, message string, retryable bool) Response {
	return Response{
		OK: false,
		Error: &ErrorObject{
			Code:      code,
			Message:   message,
			Retryable: retryable,
		},
	}
}

// writeFabricatedError encodes a Response into guest memory so the guest's
// JSON parser can pick it up using the same code path as a successful call.
func writeFabricatedError(mod api.Module, packed *uint64, code, msg string, cause error) {
	resp := errorResponse(code, msg, false)
	if cause != nil {
		resp.Error.Message = fmt.Sprintf("%s: %v", msg, cause)
	}
	body, err := MarshalResponse(resp)
	if err != nil {
		return
	}
	ptr, ok := writeResponseToGuest(context.Background(), mod, body)
	if !ok {
		return
	}
	*packed = PackPointerLength(ptr, uint32(len(body)))
}

// writeResponseToGuest copies a host-prepared buffer into guest memory.
// The pointer returned must be freed by the guest (every export contract
// requires the guest to call pi_extension_free on returned pointers).
func writeResponseToGuest(ctx context.Context, mod api.Module, data []byte) (uint32, bool) {
	alloc := mod.ExportedFunction(ExportMalloc)
	if alloc == nil {
		alloc = mod.ExportedFunction(ExportMallocAlias)
	}
	if alloc == nil {
		return 0, false
	}
	results, err := alloc.Call(ctx, uint64(len(data)))
	if err != nil || len(results) == 0 {
		return 0, false
	}
	ptr := api.DecodeU32(results[0])
	if ptr == 0 {
		return 0, false
	}
	mem := mod.Memory()
	if mem == nil {
		return 0, false
	}
	if !mem.Write(ptr, data) {
		return 0, false
	}
	return ptr, true
}
