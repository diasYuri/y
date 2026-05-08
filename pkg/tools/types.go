package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	policypkg "github.com/yuri/y/internal/policy"
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
	ContentText ContentType = "text"
)

// ContentBlock is a structured tool response block.
type ContentBlock struct {
	Type ContentType `json:"type"`
	Text string      `json:"text,omitempty"`
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
	ID            string                        `json:"id,omitempty"`
	Name          string                        `json:"name"`
	Arguments     json.RawMessage               `json:"arguments,omitempty"`
	WorkspaceRoot string                        `json:"workspace_root,omitempty"`
	Approval      *policypkg.ApprovalResolution `json:"approval,omitempty"`
}

// ToolResponse is the structured result returned by a tool.
type ToolResponse struct {
	Content []ContentBlock  `json:"content,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	Details json.RawMessage `json:"details,omitempty"`
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

var (
	ErrToolNotFound          = errors.New("tool not found")
	ErrToolAlreadyRegistered = errors.New("tool already registered")
	ErrInvalidTool           = errors.New("invalid tool")
	ErrLimitExceeded         = errors.New("tool limit exceeded")
	ErrPolicyDenied          = errors.New("tool denied by policy")
	ErrApprovalRequired      = errors.New("tool requires approval")
)

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
