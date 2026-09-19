package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/diasYuri/y/pkg/policy"
)

// Capability names a permission a tool needs before it can run.
type Capability string

const (
	CapabilityFilesystemRead   Capability = "filesystem.read"
	CapabilityFilesystemWrite  Capability = "filesystem.write"
	CapabilityFilesystemList   Capability = "filesystem.list"
	CapabilityFilesystemSearch Capability = "filesystem.search"
	CapabilityProcessExec      Capability = "process.exec"
	CapabilityGitRead          Capability = "git.read"
	CapabilityGitWrite         Capability = "git.write"
)

// ContentType identifies the payload kind in a tool response.
type ContentType string

const (
	ContentText  ContentType = "text"
	ContentImage ContentType = "image"
)

// ContentBlock is a structured tool response block.
type ContentBlock struct {
	Type             ContentType     `json:"type"`
	Text             string          `json:"text,omitempty"`
	ImageData        []byte          `json:"image_data,omitempty"`
	ImageMIMEType    string          `json:"image_mime_type,omitempty"`
	Details          json.RawMessage `json:"details,omitempty"`
	ProviderMetadata json.RawMessage `json:"provider_metadata,omitempty"`
}

// ToolLimits declares input, output, and tool-specific byte limits.
type ToolLimits struct {
	MaxInputBytes         int64 `json:"max_input_bytes,omitempty"`
	MaxOutputBytes        int64 `json:"max_output_bytes,omitempty"`
	MaxCommandOutputBytes int64 `json:"max_command_output_bytes,omitempty"`
	CommandTimeoutSeconds int64 `json:"command_timeout_seconds,omitempty"`
	MaxFileReadBytes      int64 `json:"max_file_read_bytes,omitempty"`
	MaxFileWriteBytes     int64 `json:"max_file_write_bytes,omitempty"`
	MaxEntries            int   `json:"max_entries,omitempty"`
	MaxMatches            int   `json:"max_matches,omitempty"`
	MaxLineBytes          int64 `json:"max_line_bytes,omitempty"`
}

// ExecutionMode controls whether a tool runs sequentially or in parallel.
type ExecutionMode string

const (
	ExecutionSequential ExecutionMode = "sequential"
	ExecutionParallel   ExecutionMode = "parallel"
)

// ToolDescriptor describes a callable native tool.
type ToolDescriptor struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	InputSchema   json.RawMessage `json:"input_schema,omitempty"`
	Capabilities  []Capability    `json:"capabilities,omitempty"`
	Limits        ToolLimits      `json:"limits,omitempty"`
	Sensitive     bool            `json:"sensitive,omitempty"`
	ExecutionMode ExecutionMode   `json:"execution_mode,omitempty"`
}

// ToolRequest is the normalized invocation passed to a tool handler.
type ToolRequest struct {
	ID                     string                     `json:"id,omitempty"`
	Name                   string                     `json:"name"`
	Arguments              json.RawMessage            `json:"arguments,omitempty"`
	WorkspaceRoot          string                     `json:"workspace_root,omitempty"`
	ProjectID              string                     `json:"project_id,omitempty"`
	Approval               *policy.ApprovalResolution `json:"approval,omitempty"`
	Identity               policy.Identity            `json:"identity,omitempty"`
	RequestID              string                     `json:"request_id,omitempty"`
	RunID                  string                     `json:"run_id,omitempty"`
	TurnID                 string                     `json:"turn_id,omitempty"`
	PolicyVersion          string                     `json:"policy_version,omitempty"`
	AuthorizationExpiresAt time.Time                  `json:"authorization_expires_at,omitempty"`
	IdempotencyKey         string                     `json:"idempotency_key,omitempty"`
	RequiredCapabilities   []Capability               `json:"required_capabilities,omitempty"`
	// Progress receives best-effort incremental updates. It is intentionally
	// process-local and omitted from JSON when the request crosses a transport.
	Progress func(ToolProgress) `json:"-"`
}

// ToolProgress is an incremental, transport-neutral tool update.
type ToolProgress struct {
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Stream     string          `json:"stream,omitempty"`
	Text       string          `json:"text,omitempty"`
	Details    json.RawMessage `json:"details,omitempty"`
	Done       bool            `json:"done,omitempty"`
}

// ToolResponse is the structured result returned by a tool.
type ToolResponse struct {
	Content  []ContentBlock  `json:"content,omitempty"`
	IsError  bool            `json:"is_error,omitempty"`
	Details  json.RawMessage `json:"details,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Logs     []LogEntry      `json:"logs,omitempty"`
}

// LogEntry is an optional structured diagnostic emitted during execution.
type LogEntry struct {
	Timestamp time.Time       `json:"timestamp"`
	Level     string          `json:"level,omitempty"`
	Message   string          `json:"message"`
	Details   json.RawMessage `json:"details,omitempty"`
}

// ToolHandler executes a tool call.
type ToolHandler interface {
	Handle(ctx context.Context, req ToolRequest) (ToolResponse, error)
}

// ToolHandlerFunc adapts a function to ToolHandler.
type ToolHandlerFunc func(ctx context.Context, req ToolRequest) (ToolResponse, error)

// Handle executes f.
func (f ToolHandlerFunc) Handle(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	return f(ctx, req)
}

// Executor is a replaceable execution backend. Local handlers, remote workers
// and sandboxes can all implement it while preserving the same cancellation,
// idempotency and progress fields in ToolRequest.
type Executor interface {
	Execute(context.Context, ToolRequest) (ToolResponse, error)
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc func(context.Context, ToolRequest) (ToolResponse, error)

// Execute calls f.
func (f ExecutorFunc) Execute(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	return f(ctx, req)
}

// ExecutorHandler adapts an execution backend into a registry handler. It is
// the standard path for remote execution: transport code implements Executor,
// then the local registry still enforces policy and idempotency before it runs.
type ExecutorHandler struct{ Executor Executor }

// Handle delegates to the configured backend.
func (h ExecutorHandler) Handle(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if h.Executor == nil {
		return ToolResponse{}, toolError("executor_unavailable", "tool executor is nil", ErrInvalidTool)
	}
	return h.Executor.Execute(ctx, req)
}

var (
	ErrToolNotFound          = errors.New("tool not found")
	ErrToolAlreadyRegistered = errors.New("tool already registered")
	ErrInvalidTool           = errors.New("invalid tool")
	ErrLimitExceeded         = errors.New("tool limit exceeded")
	ErrPolicyDenied          = errors.New("tool denied by policy")
	ErrApprovalRequired      = errors.New("tool requires approval")
)

// ApprovalError carries the exact authorization request needed to resume a
// blocked tool call on another worker.
type ApprovalError struct {
	Request policy.ApprovalRequest
	Cause   error
}

func (e *ApprovalError) Error() string {
	if e == nil {
		return ""
	}
	if e.Request.Reason != "" {
		return e.Request.Reason
	}
	return ErrApprovalRequired.Error()
}

func (e *ApprovalError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrApprovalRequired
	}
	return e.Cause
}

// Error categorizes a tool failure while preserving its cause.
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "tool error"
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func toolError(code, message string, cause error) error {
	return &Error{Code: code, Message: message, Cause: cause}
}

func textResponse(text string, details any) (ToolResponse, error) {
	resp := ToolResponse{Content: []ContentBlock{{Type: ContentText, Text: text}}}
	if details != nil {
		raw, err := json.Marshal(details)
		if err != nil {
			return ToolResponse{}, fmt.Errorf("marshal tool details: %w", err)
		}
		resp.Details = raw
	}
	return resp, nil
}
