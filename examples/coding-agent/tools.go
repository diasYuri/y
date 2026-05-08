package main

import (
	"context"

	"github.com/yuri/y/pkg/tools"
)

// permissivePolicy allows all tool operations without interactive approval.
// Suitable for local development; use WorkspacePolicy() in production.
var permissivePolicy = tools.PolicyFunc(func(ctx context.Context, req tools.PolicyRequest) (tools.PolicyDecision, error) {
	return tools.PolicyDecision{Kind: tools.DecisionAllow}, nil
})

// registerCodingTools registers the y SDK built-in tools.
//
// The SDK provides pre-built tool families:
//   - RegisterFilesystem: read_file, write_file, list_files, search, edit, patch
//   - RegisterShell:      run_command (subprocess execution)
//   - RegisterGit:        git_status, git_diff, git_commit
func registerCodingTools(registry *tools.Registry, workspaceRoot string) error {
	if err := tools.RegisterFilesystem(registry, tools.FilesystemOptions{
		WorkspaceRoot: workspaceRoot,
		Policy:        permissivePolicy,
	}); err != nil {
		return err
	}
	if err := tools.RegisterShell(registry, tools.ShellOptions{
		WorkspaceRoot: workspaceRoot,
		Policy:        permissivePolicy,
	}); err != nil {
		return err
	}
	return tools.RegisterGit(registry, tools.GitOptions{
		WorkspaceRoot: workspaceRoot,
		Policy:        permissivePolicy,
	})
}
