package wasm

import (
	"errors"
	"fmt"
)

// ErrHostUnavailable indicates that the binary was built without the
// feature_wasm_ext tag and therefore cannot instantiate WASM modules.
var ErrHostUnavailable = errors.New("wasm extension host unavailable in this build (missing feature_wasm_ext)")

// ErrExtensionNotFound is returned when an extension is referenced by id but
// has not been discovered.
var ErrExtensionNotFound = errors.New("extension not found")

// Sentinel error codes embedded in *ExtensionError. The set is
// deliberately small so callers can switch on it without depending on
// the concrete error type.
const (
	CodeABIMismatch       = "abi_mismatch"
	CodeCapabilityDenied  = "capability_denied"
	CodePolicyDenied      = "policy_denied"
	CodeOutputTooLarge    = "output_too_large"
	CodeInputTooLarge     = "input_too_large"
	CodeLogQuotaExceeded  = "log_quota_exceeded"
	CodeHostCallQuota     = "host_call_quota_exceeded"
	CodeTimeout           = "timeout"
	CodeFuelExhausted     = "fuel_exhausted"
	CodeMemoryLimit       = "memory_limit"
	CodeTrap              = "trap"
	CodeInvalidArgument   = "invalid_argument"
	CodeInternal          = "internal"
	CodeToolNotFound      = "tool_not_found"
	CodeUnsupportedHostOp = "unsupported_host_op"
	CodeManifestUnknown   = "manifest_unknown"
)

// ExtensionError is the canonical runtime failure surfaced by the host. It
// implements both error and json.Marshaler-friendly fields so structured
// logs can record it without reflection.
type ExtensionError struct {
	ExtensionID string
	Code        string
	Message     string
	Retryable   bool
	Cause       error
}

// Error reports the failure in a human readable form.
func (e *ExtensionError) Error() string {
	switch {
	case e == nil:
		return "<nil>"
	case e.ExtensionID != "":
		return fmt.Sprintf("extension %s: %s: %s", e.ExtensionID, e.Code, e.Message)
	default:
		return fmt.Sprintf("extension: %s: %s", e.Code, e.Message)
	}
}

// Unwrap exposes the underlying cause when present.
func (e *ExtensionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// IsCode reports whether err is an *ExtensionError carrying the given code.
func IsCode(err error, code string) bool {
	var ee *ExtensionError
	if !errors.As(err, &ee) {
		return false
	}
	return ee.Code == code
}

// newExtensionError is a small constructor that tests rely on.
func newExtensionError(id, code, msg string, cause error) *ExtensionError {
	return &ExtensionError{
		ExtensionID: id,
		Code:        code,
		Message:     msg,
		Cause:       cause,
	}
}

// ManifestError captures a manifest parse or validation failure with the
// originating file path and an optional cause.
type ManifestError struct {
	Path    string
	Field   string
	Line    int
	Message string
	Cause   error
}

// Error returns a human-readable description of the manifest failure.
func (e *ManifestError) Error() string {
	prefix := "manifest"
	if e.Path != "" {
		prefix = fmt.Sprintf("manifest %q", e.Path)
	}
	switch {
	case e.Line > 0 && e.Field != "":
		return fmt.Sprintf("%s line %d field %q: %s", prefix, e.Line, e.Field, e.Message)
	case e.Line > 0:
		return fmt.Sprintf("%s line %d: %s", prefix, e.Line, e.Message)
	case e.Field != "":
		return fmt.Sprintf("%s field %q: %s", prefix, e.Field, e.Message)
	default:
		return fmt.Sprintf("%s: %s", prefix, e.Message)
	}
}

// Unwrap returns the underlying cause if any.
func (e *ManifestError) Unwrap() error { return e.Cause }

// newManifestError is a small constructor that callers use to surface a
// manifest-level failure without nil-checking optional fields.
func newManifestError(path, field, message string, line int, cause error) error {
	return &ManifestError{Path: path, Field: field, Message: message, Line: line, Cause: cause}
}
