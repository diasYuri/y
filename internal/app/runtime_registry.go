package app

import (
	"context"

	"github.com/yuri/y/internal/feature"
	"github.com/yuri/y/pkg/tools"
)

func buildRuntimeRegistry(
	ctx context.Context,
	compiled *feature.Registry,
	cwd string,
	policyEngine tools.Policy,
	approvalHandler tools.ApprovalHandler,
) (*tools.Registry, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if policyEngine == nil {
		policyEngine = tools.WorkspacePolicy()
	}

	options := []tools.RegistryOption{tools.WithPolicy(policyEngine)}
	if approvalHandler != nil {
		options = append(options, tools.WithApprovalHandler(approvalHandler))
	}
	registry := tools.NewRegistry(options...)
	if compiled == nil {
		return registry, nil
	}

	if compiled.Has(feature.KindFeature, "filesystem") {
		if err := tools.RegisterFilesystem(registry, tools.FilesystemOptions{
			WorkspaceRoot: cwd,
			Policy:        policyEngine,
		}); err != nil {
			return nil, err
		}
	}
	if compiled.Has(feature.KindFeature, "git") {
		if err := tools.RegisterGit(registry, tools.GitOptions{
			WorkspaceRoot: cwd,
			Policy:        policyEngine,
		}); err != nil {
			return nil, err
		}
	}
	if compiled.Has(feature.KindFeature, "shell") {
		if err := tools.RegisterShell(registry, tools.ShellOptions{
			WorkspaceRoot: cwd,
			Policy:        policyEngine,
		}); err != nil {
			return nil, err
		}
	}
	return registry, nil
}
