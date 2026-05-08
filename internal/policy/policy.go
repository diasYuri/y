package policy

import (
	"context"
	"fmt"
)

// DecisionKind classifies the outcome of a policy evaluation.
type DecisionKind string

const (
	DecisionAllow           DecisionKind = "allow"
	DecisionDeny            DecisionKind = "deny"
	DecisionRequireApproval DecisionKind = "require_approval"
)

// ApprovalMode identifies how an approval request will be surfaced.
type ApprovalMode string

const (
	ApprovalModeHeadless ApprovalMode = "headless"
)

// ApprovalState describes the current status of an approval resolution.
type ApprovalState string

const (
	ApprovalPending  ApprovalState = "pending"
	ApprovalApproved ApprovalState = "approved"
	ApprovalDenied   ApprovalState = "denied"
)

// ApprovalResolution captures an approval outcome supplied by the caller.
type ApprovalResolution struct {
	Mode   ApprovalMode  `json:"mode,omitempty"`
	State  ApprovalState `json:"state"`
	Reason string        `json:"reason,omitempty"`
	Actor  string        `json:"actor,omitempty"`
}

// ApprovalRequest is returned when a decision requires user approval.
type ApprovalRequest struct {
	Mode          ApprovalMode `json:"mode"`
	ToolName      string       `json:"tool_name,omitempty"`
	Capability    string       `json:"capability,omitempty"`
	WorkspaceRoot string       `json:"workspace_root,omitempty"`
	Path          string       `json:"path,omitempty"`
	ResolvedPath  string       `json:"resolved_path,omitempty"`
	Reason        string       `json:"reason,omitempty"`
}

// Decision is the typed authorization result for a concrete operation.
type Decision struct {
	Kind     DecisionKind     `json:"kind"`
	Reason   string           `json:"reason,omitempty"`
	Approval *ApprovalRequest `json:"approval,omitempty"`
}

// Request describes a concrete operation requiring authorization.
type Request struct {
	ToolName         string              `json:"tool_name,omitempty"`
	Capability       string              `json:"capability,omitempty"`
	WorkspaceRoot    string              `json:"workspace_root,omitempty"`
	Path             string              `json:"path,omitempty"`
	ResolvedPath     string              `json:"resolved_path,omitempty"`
	EscapesWorkspace bool                `json:"escapes_workspace,omitempty"`
	Sensitive        bool                `json:"sensitive,omitempty"`
	Approval         *ApprovalResolution `json:"approval,omitempty"`
}

// Config controls the default rules used by the engine.
type Config struct {
	ApprovalMode                ApprovalMode `json:"approval_mode,omitempty"`
	DenyEscapesWorkspace        bool         `json:"deny_escaped_workspace,omitempty"`
	RequireApprovalForSensitive bool         `json:"require_approval_for_sensitive,omitempty"`
}

// DefaultConfig returns the baseline headless configuration.
func DefaultConfig() Config {
	return Config{
		ApprovalMode:                ApprovalModeHeadless,
		DenyEscapesWorkspace:        true,
		RequireApprovalForSensitive: true,
	}
}

// Engine evaluates policy requests.
type Engine interface {
	Decide(ctx context.Context, req Request) (Decision, error)
}

// Func adapts a function to Engine.
type Func func(ctx context.Context, req Request) (Decision, error)

// Decide calls f.
func (f Func) Decide(ctx context.Context, req Request) (Decision, error) {
	return f(ctx, req)
}

type engine struct {
	cfg Config
}

// NewEngine creates a rule-based policy engine with the given config.
func NewEngine(cfg Config) Engine {
	base := DefaultConfig()
	if cfg.ApprovalMode != "" {
		base.ApprovalMode = cfg.ApprovalMode
	}
	if cfg.DenyEscapesWorkspace {
		base.DenyEscapesWorkspace = true
	}
	if cfg.RequireApprovalForSensitive {
		base.RequireApprovalForSensitive = true
	}
	return &engine{cfg: base}
}

// Decide applies the default workspace and approval rules.
func (e *engine) Decide(ctx context.Context, req Request) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	if req.EscapesWorkspace && e.cfg.DenyEscapesWorkspace {
		return Decision{
			Kind:   DecisionDeny,
			Reason: "workspace escape is denied",
		}, nil
	}
	if req.Approval != nil {
		switch req.Approval.State {
		case ApprovalApproved:
			return Decision{Kind: DecisionAllow}, nil
		case ApprovalDenied:
			reason := req.Approval.Reason
			if reason == "" {
				reason = "approval denied"
			}
			return Decision{Kind: DecisionDeny, Reason: reason}, nil
		case ApprovalPending, "":
			return approvalRequiredDecision(req, approvalMode(req.Approval.Mode, e.cfg.ApprovalMode)), nil
		default:
			return Decision{
				Kind:   DecisionDeny,
				Reason: fmt.Sprintf("unsupported approval state %q", req.Approval.State),
			}, nil
		}
	}
	if req.Sensitive && e.cfg.RequireApprovalForSensitive {
		return approvalRequiredDecision(req, e.cfg.ApprovalMode), nil
	}
	return Decision{Kind: DecisionAllow}, nil
}

func approvalMode(requested, fallback ApprovalMode) ApprovalMode {
	if requested != "" {
		return requested
	}
	if fallback != "" {
		return fallback
	}
	return ApprovalModeHeadless
}

func approvalRequiredDecision(req Request, mode ApprovalMode) Decision {
	return Decision{
		Kind: DecisionRequireApproval,
		Approval: &ApprovalRequest{
			Mode:          approvalMode(mode, ApprovalModeHeadless),
			ToolName:      req.ToolName,
			Capability:    req.Capability,
			WorkspaceRoot: req.WorkspaceRoot,
			Path:          req.Path,
			ResolvedPath:  req.ResolvedPath,
			Reason:        "approval required before executing a sensitive operation",
		},
	}
}
