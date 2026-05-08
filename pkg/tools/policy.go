package tools

import (
	"context"
	"fmt"

	policypkg "github.com/yuri/y/internal/policy"
)

// PolicyDecision is the typed authorization result for a concrete tool operation.
type PolicyDecision = policypkg.Decision

const (
	DecisionAllow           = policypkg.DecisionAllow
	DecisionDeny            = policypkg.DecisionDeny
	DecisionRequireApproval = policypkg.DecisionRequireApproval
	ApprovalModeHeadless    = policypkg.ApprovalModeHeadless
	ApprovalPending         = policypkg.ApprovalPending
	ApprovalApproved        = policypkg.ApprovalApproved
	ApprovalDenied          = policypkg.ApprovalDenied
)

// PolicyRequest describes a concrete operation requiring authorization.
type PolicyRequest = policypkg.Request

// ApprovalRequest describes a pending approval surfaced to the caller.
type ApprovalRequest = policypkg.ApprovalRequest

// ApprovalResolution captures a resolved approval supplied by the caller.
type ApprovalResolution = policypkg.ApprovalResolution

// Policy authorizes concrete tool operations.
type Policy = policypkg.Engine

// PolicyFunc adapts a function to Policy.
type PolicyFunc = policypkg.Func

// WorkspacePolicy returns the default engine used by tools when no policy is injected.
func WorkspacePolicy() Policy {
	return policypkg.NewEngine(policypkg.DefaultConfig())
}

func decide(ctx context.Context, policy Policy, req PolicyRequest) (PolicyDecision, error) {
	if err := ctx.Err(); err != nil {
		return PolicyDecision{}, err
	}
	if policy == nil {
		policy = WorkspacePolicy()
	}
	decision, err := policy.Decide(ctx, req)
	if err != nil {
		return PolicyDecision{}, err
	}
	return decision, nil
}

func authorize(ctx context.Context, policy Policy, req PolicyRequest) error {
	decision, err := decide(ctx, policy, req)
	if err != nil {
		return err
	}
	switch decision.Kind {
	case DecisionAllow:
		return nil
	case DecisionRequireApproval:
		reason := decision.Reason
		if reason == "" && decision.Approval != nil {
			reason = decision.Approval.Reason
		}
		message := fmt.Sprintf("%s requires approval for %s", req.ToolName, req.Path)
		if decision.Approval != nil && decision.Approval.Mode != "" {
			message = fmt.Sprintf("%s requires %s approval for %s", req.ToolName, decision.Approval.Mode, req.Path)
		}
		if reason != "" {
			message = fmt.Sprintf("%s: %s", message, reason)
		}
		return toolError("approval_required", message, ErrApprovalRequired)
	case DecisionDeny:
		message := fmt.Sprintf("%s denied for %s", req.ToolName, req.Path)
		if decision.Reason != "" {
			message = fmt.Sprintf("%s: %s", message, decision.Reason)
		}
		return toolError("policy_denied", message, ErrPolicyDenied)
	default:
		return toolError("policy_denied", fmt.Sprintf("%s policy returned unsupported decision %q", req.ToolName, decision.Kind), ErrPolicyDenied)
	}
}
