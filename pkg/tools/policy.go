package tools

import (
	"context"
	"fmt"

	policypkg "github.com/diasYuri/y/pkg/policy"
)

type toolRequestContextKey struct{}

func withToolRequestContext(ctx context.Context, req ToolRequest) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, toolRequestContextKey{}, req)
}

func policyRequestWithToolContext(ctx context.Context, req PolicyRequest) PolicyRequest {
	toolRequest, ok := ctx.Value(toolRequestContextKey{}).(ToolRequest)
	if !ok {
		return req
	}
	if toolRequest.Identity.TenantID != "" || toolRequest.Identity.CallerID != "" || len(toolRequest.Identity.Capabilities) > 0 {
		req.Identity = toolRequest.Identity
	}
	if req.RequestID == "" {
		req.RequestID = toolRequest.RequestID
	}
	if req.RunID == "" {
		req.RunID = toolRequest.RunID
	}
	if req.TurnID == "" {
		req.TurnID = toolRequest.TurnID
	}
	if req.ToolCallID == "" {
		req.ToolCallID = toolRequest.ID
	}
	if req.PolicyVersion == "" {
		req.PolicyVersion = toolRequest.PolicyVersion
	}
	if len(req.Arguments) == 0 {
		req.Arguments = toolRequest.Arguments
	}
	if len(req.RequiredCapabilities) == 0 {
		req.RequiredCapabilities = capabilityNames(toolRequest.RequiredCapabilities)
	}
	if req.Approval == nil {
		req.Approval = toolRequest.Approval
	}
	return req
}

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
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return PolicyDecision{}, err
		}
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
	if ctx == nil {
		ctx = context.Background()
	}
	req = policyRequestWithToolContext(ctx, req)
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
