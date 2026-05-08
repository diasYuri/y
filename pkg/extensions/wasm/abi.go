package wasm

import (
	"encoding/json"
	"fmt"
)

// Exported function names every guest must provide. They form the
// pi.wasm.v1 contract described in extension-wasm.md §10.
const (
	ExportABIVersion = "pi_extension_abi_version"
	ExportInit       = "pi_extension_init"
	ExportHandle     = "pi_extension_handle"
	ExportShutdown   = "pi_extension_shutdown"
	ExportFree       = "pi_extension_free"

	// ExportMalloc lets the host allocate guest memory before invoking
	// init/handle/shutdown. Guests may either expose this name or the
	// "malloc" alias used by TinyGo's `-target=wasi`.
	ExportMalloc      = "pi_extension_malloc"
	ExportMallocAlias = "malloc"

	// HostModuleName is the WebAssembly module that exposes the host
	// functions described in §14.
	HostModuleName = "pi_host"

	// FuncHostCall is the unified host call entry. Guests pass JSON
	// envelopes through it and receive structured responses.
	FuncHostCall = "pi_host_call"
	// FuncHostLog records an informational message in the extension log
	// stream. Logs are bounded by the host limits.
	FuncHostLog = "pi_host_log"
	// FuncHostNow returns wall-clock time in milliseconds since epoch.
	FuncHostNow = "pi_host_now"
)

// SupportedABIVersion is the integer value returned by
// pi_extension_abi_version() that the host accepts. Bumping this constant is
// a breaking change.
const SupportedABIVersion uint32 = 1

// LogLevel maps the integer levels accepted by pi_host_log onto a textual
// representation suitable for log routing.
type LogLevel uint32

const (
	LogLevelDebug LogLevel = 0
	LogLevelInfo  LogLevel = 1
	LogLevelWarn  LogLevel = 2
	LogLevelError LogLevel = 3
)

// String returns the canonical level name.
func (l LogLevel) String() string {
	switch l {
	case LogLevelDebug:
		return "debug"
	case LogLevelInfo:
		return "info"
	case LogLevelWarn:
		return "warn"
	case LogLevelError:
		return "error"
	default:
		return fmt.Sprintf("level%d", uint32(l))
	}
}

// Envelope is the JSON payload exchanged with the guest. It mirrors the
// shape documented in extension-wasm.md §11.
type Envelope struct {
	APIVersion  string          `json:"api_version"`
	RequestID   string          `json:"request_id,omitempty"`
	ExtensionID string          `json:"extension_id,omitempty"`
	Kind        EnvelopeKind    `json:"kind"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

// EnvelopeKind describes the type of guest call.
type EnvelopeKind string

const (
	KindInit     EnvelopeKind = "init"
	KindToolCall EnvelopeKind = "tool_call"
	KindShutdown EnvelopeKind = "shutdown"

	// Host call kinds, used by pi_host_call:
	KindHostLog     EnvelopeKind = "log"
	KindHostNow     EnvelopeKind = "now"
	KindHostCapInfo EnvelopeKind = "capability_info"
	// KindToolInvoke lets the guest call back into a host-side tool that
	// is registered through the y_tools capability. The payload mirrors
	// the y-side ToolRequest schema.
	KindToolInvoke      EnvelopeKind = "tool_invoke"
	KindProcessExec     EnvelopeKind = "process_exec"
	KindProviderRequest EnvelopeKind = "provider_request"
)

// Response is the JSON envelope returned from any guest export. Successful
// calls populate Payload; failures populate Error.
type Response struct {
	RequestID string          `json:"request_id,omitempty"`
	OK        bool            `json:"ok"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     *ErrorObject    `json:"error,omitempty"`
}

// ErrorObject is the structured error embedded in a Response.
type ErrorObject struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

// InitRequest is the payload for KindInit.
type InitRequest struct {
	YVersion     string         `json:"y_version"`
	ExtensionID  string         `json:"extension_id"`
	Capabilities []string       `json:"capabilities,omitempty"`
	Limits       LimitsSnapshot `json:"limits,omitzero"`
	Config       map[string]any `json:"config,omitempty"`
}

// InitResponse is the structured payload returned by pi_extension_init.
type InitResponse struct {
	Tools     []ToolDescriptor     `json:"tools,omitempty"`
	Commands  []CommandDescriptor  `json:"commands,omitempty"`
	Providers []ProviderDescriptor `json:"providers,omitempty"`
}

// ToolDescriptor reports a tool the guest registered during init.
type ToolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// CommandDescriptor is reserved for V2 (commands).
type CommandDescriptor struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ProviderDescriptor is reserved for V2 (providers).
type ProviderDescriptor struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// ToolCallRequest is the payload for KindToolCall.
type ToolCallRequest struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ToolCallResponse is the payload returned by the guest on a tool call.
type ToolCallResponse struct {
	Content []ContentBlock `json:"content,omitempty"`
}

// ContentBlock is a typed piece of tool output. Only "text" is required for
// the V1 ABI; other kinds round-trip through Data so future-compatible
// extensions stay forward-compatible.
type ContentBlock struct {
	Type string          `json:"type"`
	Text string          `json:"text,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// LimitsSnapshot is the limits view shipped to guests during init so they
// can stop short before the host kills them.
type LimitsSnapshot struct {
	MaxInputBytes  uint32 `json:"max_input_bytes,omitempty"`
	MaxOutputBytes uint32 `json:"max_output_bytes,omitempty"`
	TimeoutMS      uint32 `json:"timeout_ms,omitempty"`
}

// HostCallRequest is the envelope guests send through pi_host_call.
type HostCallRequest struct {
	Kind    EnvelopeKind    `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// HostLogPayload is the body of KindHostLog host calls.
type HostLogPayload struct {
	Level   uint32 `json:"level"`
	Message string `json:"message"`
}

// HostNowPayload is the response body for KindHostNow.
type HostNowPayload struct {
	UnixMillis int64 `json:"unix_millis"`
}

// HostToolInvokePayload is the body of KindToolInvoke host calls.
type HostToolInvokePayload struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// HostToolInvokeResult is the structured response returned to guests after
// a tool invocation.
type HostToolInvokeResult struct {
	Content []ContentBlock `json:"content,omitempty"`
}

// HostProcessExecPayload is the body of KindProcessExec host calls.
type HostProcessExecPayload struct {
	Command          []string          `json:"command"`
	Env              map[string]string `json:"env,omitempty"`
	TimeoutMS        uint32            `json:"timeout_ms,omitempty"`
	MaxOutputBytes   uint32            `json:"max_output_bytes,omitempty"`
	WorkingDirectory string            `json:"cwd,omitempty"`
}

// HostProcessExecResult reports the stdout and stderr of a process exec.
type HostProcessExecResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	TimedOut  bool   `json:"timed_out,omitempty"`
}

// HostProviderRequestPayload is the body of KindProviderRequest host calls.
type HostProviderRequestPayload struct {
	ProviderID string          `json:"provider_id"`
	Method     string          `json:"method"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// HostProviderResponse reports the result of a provider request.
type HostProviderResponse struct {
	ContentType string `json:"content_type,omitempty"`
	Body        string `json:"body"`
}

// CapabilityInfoPayload reports which capabilities are currently active for
// the calling extension. Guests should treat absent entries as denied.
type CapabilityInfoPayload struct {
	Granted []string `json:"granted"`
}

// MarshalEnvelope encodes an Envelope into the wire format. It validates
// the payload size against the supplied cap so the host can short-circuit
// before the guest has a chance to read it.
func MarshalEnvelope(env Envelope, max uint32) ([]byte, error) {
	if env.APIVersion == "" {
		env.APIVersion = SupportedAPIVersion
	}
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	if max > 0 && uint32(len(data)) > max {
		return nil, fmt.Errorf("envelope size %d exceeds limit %d", len(data), max)
	}
	return data, nil
}

// UnmarshalResponse parses a guest response from JSON.
func UnmarshalResponse(buf []byte) (Response, error) {
	var resp Response
	if len(buf) == 0 {
		return Response{OK: true}, nil
	}
	if err := json.Unmarshal(buf, &resp); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}
	return resp, nil
}

// MarshalResponse encodes a Response. Used by the host when fabricating
// pre-call rejections (e.g. capability denied) before reaching the guest.
func MarshalResponse(resp Response) ([]byte, error) {
	return json.Marshal(resp)
}

// PackPointerLength encodes a guest pointer/length pair into the uint64
// return value defined in §10 (high 32 bits = ptr, low 32 bits = length).
func PackPointerLength(ptr, length uint32) uint64 {
	return (uint64(ptr) << 32) | uint64(length)
}

// UnpackPointerLength splits the encoded uint64 returned by guest exports.
func UnpackPointerLength(packed uint64) (uint32, uint32) {
	ptr := uint32(packed >> 32)
	length := uint32(packed & 0xFFFFFFFF)
	return ptr, length
}
