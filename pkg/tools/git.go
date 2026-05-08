package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// GitOptions configures the git tools.
type GitOptions struct {
	WorkspaceRoot string
	Policy        Policy
	Limits        ToolLimits
}

// RegisterGit registers git_status, git_diff, and git_commit.
func RegisterGit(r *Registry, opts GitOptions) error {
	if r == nil {
		return fmt.Errorf("tool registry is nil")
	}
	git := newGitTool(opts)
	for _, def := range []struct {
		desc    ToolDescriptor
		handler ToolHandler
	}{
		{git.statusDescriptor(), ToolHandlerFunc(git.status)},
		{git.diffDescriptor(), ToolHandlerFunc(git.diff)},
		{git.logDescriptor(), ToolHandlerFunc(git.log)},
		{git.branchDescriptor(), ToolHandlerFunc(git.branch)},
		{git.checkoutDescriptor(), ToolHandlerFunc(git.checkout)},
		{git.commitDescriptor(), ToolHandlerFunc(git.commit)},
	} {
		if err := r.Add(def.desc, def.handler); err != nil {
			return err
		}
	}
	return nil
}

type gitTool struct {
	workspaceRoot string
	policy        Policy
	limits        ToolLimits
}

func newGitTool(opts GitOptions) *gitTool {
	return &gitTool{
		workspaceRoot: opts.WorkspaceRoot,
		policy:        opts.Policy,
		limits:        commandLimits(opts.Limits),
	}
}

func (t *gitTool) statusDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "git_status",
		Description:  "Read git status in short, branch-aware form.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"cwd":{"type":"string"},"max_output_bytes":{"type":"integer","minimum":1}},"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityGitRead},
		Limits:       t.limits,
	}
}

func (t *gitTool) diffDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "git_diff",
		Description:  "Read git diff with output limits.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"cwd":{"type":"string"},"cached":{"type":"boolean"},"paths":{"type":"array","items":{"type":"string"}},"max_output_bytes":{"type":"integer","minimum":1}},"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityGitRead},
		Limits:       t.limits,
	}
}

func (t *gitTool) logDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "git_log",
		Description:  "Read git log with output limits. Optionally filter by path and limit the number of commits.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"cwd":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}},"max_count":{"type":"integer","minimum":1,"maximum":100},"max_output_bytes":{"type":"integer","minimum":1}},"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityGitRead},
		Limits:       t.limits,
	}
}

func (t *gitTool) branchDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "git_branch",
		Description:  "List git branches or create/delete branches.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"cwd":{"type":"string"},"list":{"type":"boolean"},"create":{"type":"string"},"delete":{"type":"string"},"max_output_bytes":{"type":"integer","minimum":1}},"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityGitRead, CapabilityGitWrite},
		Limits:       t.limits,
	}
}

func (t *gitTool) checkoutDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "git_checkout",
		Description:  "Checkout a git branch or commit.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"cwd":{"type":"string"},"ref":{"type":"string"},"create_branch":{"type":"boolean"},"paths":{"type":"array","items":{"type":"string"}},"max_output_bytes":{"type":"integer","minimum":1}},"required":["ref"],"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityGitWrite},
		Limits:       t.limits,
		Sensitive:    true,
	}
}

func (t *gitTool) commitDescriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "git_commit",
		Description:  "Create a git commit after staging explicit paths. No broad staging is used; paths must be provided.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"cwd":{"type":"string"},"message":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}},"amend":{"type":"boolean"},"max_output_bytes":{"type":"integer","minimum":1}},"required":["message"],"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityGitWrite},
		Limits:       t.limits,
		Sensitive:    true,
	}
}

type gitStatusInput struct {
	CWD            string `json:"cwd,omitempty"`
	MaxOutputBytes int64  `json:"max_output_bytes,omitempty"`
}

type gitDiffInput struct {
	CWD            string   `json:"cwd,omitempty"`
	Cached         bool     `json:"cached,omitempty"`
	Paths          []string `json:"paths,omitempty"`
	MaxOutputBytes int64    `json:"max_output_bytes,omitempty"`
}

type gitLogInput struct {
	CWD            string   `json:"cwd,omitempty"`
	Paths          []string `json:"paths,omitempty"`
	MaxCount       int      `json:"max_count,omitempty"`
	MaxOutputBytes int64    `json:"max_output_bytes,omitempty"`
}

type gitBranchInput struct {
	CWD            string `json:"cwd,omitempty"`
	List           bool   `json:"list,omitempty"`
	Create         string `json:"create,omitempty"`
	Delete         string `json:"delete,omitempty"`
	MaxOutputBytes int64  `json:"max_output_bytes,omitempty"`
}

type gitCheckoutInput struct {
	CWD            string   `json:"cwd,omitempty"`
	Ref            string   `json:"ref"`
	CreateBranch   bool     `json:"create_branch,omitempty"`
	Paths          []string `json:"paths,omitempty"`
	MaxOutputBytes int64    `json:"max_output_bytes,omitempty"`
}

type gitCommitInput struct {
	CWD            string   `json:"cwd,omitempty"`
	Message        string   `json:"message"`
	Paths          []string `json:"paths,omitempty"`
	Amend          bool     `json:"amend,omitempty"`
	MaxOutputBytes int64    `json:"max_output_bytes,omitempty"`
}

type gitDetails struct {
	CWD             string   `json:"cwd,omitempty"`
	Command         string   `json:"command"`
	Args            []string `json:"args,omitempty"`
	ExitCode        int      `json:"exit_code"`
	StdoutBytes     int64    `json:"stdout_bytes"`
	StderrBytes     int64    `json:"stderr_bytes"`
	StdoutTruncated bool     `json:"stdout_truncated,omitempty"`
	StderrTruncated bool     `json:"stderr_truncated,omitempty"`
}

func (t *gitTool) status(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, t.limits); err != nil {
		return ToolResponse{}, err
	}
	var input gitStatusInput
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &input); err != nil {
			return ToolResponse{}, toolError("invalid_arguments", "git_status arguments must be valid JSON", err)
		}
	}

	cwd, err := resolveCommandDir(t.workspaceRoot, req.WorkspaceRoot, input.CWD)
	if err != nil {
		return ToolResponse{}, err
	}
	result, err := t.runGit(ctx, cwd, []string{"status", "--short", "--branch"}, input.MaxOutputBytes, req.Approval, false, CapabilityGitRead)
	if err != nil {
		return ToolResponse{}, err
	}
	return commandResultResponse(result, gitDetails{
		CWD:             cwd,
		Command:         "git",
		Args:            []string{"status", "--short", "--branch"},
		ExitCode:        result.ExitCode,
		StdoutBytes:     result.StdoutBytes,
		StderrBytes:     result.StderrBytes,
		StdoutTruncated: result.StdoutTruncated,
		StderrTruncated: result.StderrTruncated,
	})
}

func (t *gitTool) diff(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, t.limits); err != nil {
		return ToolResponse{}, err
	}
	var input gitDiffInput
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &input); err != nil {
			return ToolResponse{}, toolError("invalid_arguments", "git_diff arguments must be valid JSON", err)
		}
	}
	cwd, err := resolveCommandDir(t.workspaceRoot, req.WorkspaceRoot, input.CWD)
	if err != nil {
		return ToolResponse{}, err
	}
	args := []string{"diff", "--no-ext-diff"}
	if input.Cached {
		args = append(args, "--cached")
	}
	if len(input.Paths) > 0 {
		args = append(args, "--")
		args = append(args, input.Paths...)
	}
	result, err := t.runGit(ctx, cwd, args, input.MaxOutputBytes, req.Approval, false, CapabilityGitRead)
	if err != nil {
		return ToolResponse{}, err
	}
	return commandResultResponse(result, gitDetails{
		CWD:             cwd,
		Command:         "git",
		Args:            args,
		ExitCode:        result.ExitCode,
		StdoutBytes:     result.StdoutBytes,
		StderrBytes:     result.StderrBytes,
		StdoutTruncated: result.StdoutTruncated,
		StderrTruncated: result.StderrTruncated,
	})
}

func (t *gitTool) commit(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, t.limits); err != nil {
		return ToolResponse{}, err
	}
	var input gitCommitInput
	if err := json.Unmarshal(req.Arguments, &input); err != nil {
		return ToolResponse{}, toolError("invalid_arguments", "git_commit arguments must be valid JSON", err)
	}
	if strings.TrimSpace(input.Message) == "" {
		return ToolResponse{}, toolError("invalid_arguments", "git_commit message is required", ErrInvalidTool)
	}
	if len(input.Paths) == 0 {
		return ToolResponse{}, toolError("invalid_arguments", "git_commit requires explicit paths; broad staging (git add -A) is not allowed", ErrInvalidTool)
	}
	cwd, err := resolveCommandDir(t.workspaceRoot, req.WorkspaceRoot, input.CWD)
	if err != nil {
		return ToolResponse{}, err
	}

	stageArgs := append([]string{"add", "--"}, input.Paths...)
	stageResult, err := t.runGit(ctx, cwd, stageArgs, input.MaxOutputBytes, req.Approval, true, CapabilityGitWrite)
	if err != nil {
		return ToolResponse{}, err
	}
	if stageResult.ExitCode != 0 || stageResult.TimedOut {
		return commandResultResponse(stageResult, gitDetails{
			CWD:             cwd,
			Command:         "git",
			Args:            stageArgs,
			ExitCode:        stageResult.ExitCode,
			StdoutBytes:     stageResult.StdoutBytes,
			StderrBytes:     stageResult.StderrBytes,
			StdoutTruncated: stageResult.StdoutTruncated,
			StderrTruncated: stageResult.StderrTruncated,
		})
	}

	commitArgs := []string{"commit", "-m", input.Message}
	if input.Amend {
		commitArgs = append(commitArgs, "--amend")
	}
	commitResult, err := t.runGit(ctx, cwd, commitArgs, input.MaxOutputBytes, req.Approval, true, CapabilityGitWrite)
	if err != nil {
		return ToolResponse{}, err
	}
	resp, err := commandResultResponse(commitResult, gitDetails{
		CWD:             cwd,
		Command:         "git",
		Args:            commitArgs,
		ExitCode:        commitResult.ExitCode,
		StdoutBytes:     commitResult.StdoutBytes,
		StderrBytes:     commitResult.StderrBytes,
		StdoutTruncated: commitResult.StdoutTruncated,
		StderrTruncated: commitResult.StderrTruncated,
	})
	if err != nil {
		return ToolResponse{}, err
	}
	return resp, nil
}

func (t *gitTool) log(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, t.limits); err != nil {
		return ToolResponse{}, err
	}
	var input gitLogInput
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &input); err != nil {
			return ToolResponse{}, toolError("invalid_arguments", "git_log arguments must be valid JSON", err)
		}
	}
	cwd, err := resolveCommandDir(t.workspaceRoot, req.WorkspaceRoot, input.CWD)
	if err != nil {
		return ToolResponse{}, err
	}
	args := []string{"log", "--oneline", "--decorate", "--graph", "--no-color"}
	if input.MaxCount > 0 {
		args = append(args, fmt.Sprintf("-%d", input.MaxCount))
	}
	if len(input.Paths) > 0 {
		args = append(args, "--")
		args = append(args, input.Paths...)
	}
	result, err := t.runGit(ctx, cwd, args, input.MaxOutputBytes, req.Approval, false, CapabilityGitRead)
	if err != nil {
		return ToolResponse{}, err
	}
	return commandResultResponse(result, gitDetails{
		CWD:             cwd,
		Command:         "git",
		Args:            args,
		ExitCode:        result.ExitCode,
		StdoutBytes:     result.StdoutBytes,
		StderrBytes:     result.StderrBytes,
		StdoutTruncated: result.StdoutTruncated,
		StderrTruncated: result.StderrTruncated,
	})
}

func (t *gitTool) branch(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, t.limits); err != nil {
		return ToolResponse{}, err
	}
	var input gitBranchInput
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &input); err != nil {
			return ToolResponse{}, toolError("invalid_arguments", "git_branch arguments must be valid JSON", err)
		}
	}
	cwd, err := resolveCommandDir(t.workspaceRoot, req.WorkspaceRoot, input.CWD)
	if err != nil {
		return ToolResponse{}, err
	}
	args := []string{"branch"}
	cap := CapabilityGitRead
	if input.List {
		args = append(args, "-a", "-vv")
	} else if input.Create != "" {
		args = append(args, input.Create)
		cap = CapabilityGitWrite
	} else if input.Delete != "" {
		args = append(args, "-D", input.Delete)
		cap = CapabilityGitWrite
	} else {
		args = append(args, "-vv")
	}
	result, err := t.runGit(ctx, cwd, args, input.MaxOutputBytes, req.Approval, cap == CapabilityGitWrite, cap)
	if err != nil {
		return ToolResponse{}, err
	}
	return commandResultResponse(result, gitDetails{
		CWD:             cwd,
		Command:         "git",
		Args:            args,
		ExitCode:        result.ExitCode,
		StdoutBytes:     result.StdoutBytes,
		StderrBytes:     result.StderrBytes,
		StdoutTruncated: result.StdoutTruncated,
		StderrTruncated: result.StderrTruncated,
	})
}

func (t *gitTool) checkout(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, t.limits); err != nil {
		return ToolResponse{}, err
	}
	var input gitCheckoutInput
	if err := json.Unmarshal(req.Arguments, &input); err != nil {
		return ToolResponse{}, toolError("invalid_arguments", "git_checkout arguments must be valid JSON", err)
	}
	if strings.TrimSpace(input.Ref) == "" {
		return ToolResponse{}, toolError("invalid_arguments", "git_checkout ref is required", ErrInvalidTool)
	}
	cwd, err := resolveCommandDir(t.workspaceRoot, req.WorkspaceRoot, input.CWD)
	if err != nil {
		return ToolResponse{}, err
	}
	args := []string{"checkout"}
	if input.CreateBranch {
		args = append(args, "-b")
	}
	args = append(args, input.Ref)
	if len(input.Paths) > 0 {
		args = append(args, "--")
		args = append(args, input.Paths...)
	}
	result, err := t.runGit(ctx, cwd, args, input.MaxOutputBytes, req.Approval, true, CapabilityGitWrite)
	if err != nil {
		return ToolResponse{}, err
	}
	return commandResultResponse(result, gitDetails{
		CWD:             cwd,
		Command:         "git",
		Args:            args,
		ExitCode:        result.ExitCode,
		StdoutBytes:     result.StdoutBytes,
		StderrBytes:     result.StderrBytes,
		StdoutTruncated: result.StdoutTruncated,
		StderrTruncated: result.StderrTruncated,
	})
}

func (t *gitTool) runGit(ctx context.Context, cwd string, args []string, maxOutputBytes int64, approval *ApprovalResolution, sensitive bool, capability Capability) (commandResult, error) {
	limits := t.limits
	if maxOutputBytes > 0 {
		limits.MaxOutputBytes = maxOutputBytes
	}
	if err := authorize(ctx, t.policy, PolicyRequest{
		ToolName:      "git",
		Capability:    string(capability),
		WorkspaceRoot: filepath.Clean(firstNonEmpty(t.workspaceRoot, cwd)),
		Path:          cwd,
		ResolvedPath:  cwd,
		Sensitive:     sensitive,
		Approval:      approval,
	}); err != nil {
		return commandResult{}, err
	}
	return executeCommand(ctx, commandSpec{
		command: "git",
		args:    args,
		cwd:     cwd,
		timeout: limits.CommandTimeoutSeconds,
		limit:   limits.MaxOutputBytes,
	})
}
