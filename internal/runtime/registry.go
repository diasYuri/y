package runtime

import (
	"context"

	"github.com/diasYuri/y/internal/feature"
	"github.com/diasYuri/y/pkg/policy"
	"github.com/diasYuri/y/pkg/telemetry"
	"github.com/diasYuri/y/pkg/tools"
	"github.com/diasYuri/y/pkg/tools/filesystem"
	"github.com/diasYuri/y/pkg/tools/git"
	"github.com/diasYuri/y/pkg/tools/shell"
)

// BuildToolRegistry creates the binary's tool registry from compiled features.
func BuildToolRegistry(
	ctx context.Context,
	compiled *feature.Registry,
	cwd string,
	policyEngine policy.Engine,
	approvalHandler tools.ApprovalHandler,
	emitter telemetry.Emitter,
) (*tools.Registry, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if policyEngine == nil {
		policyEngine = policy.NewEngine(policy.DefaultConfig())
	}

	options := []tools.RegistryOption{tools.WithPolicy(policyEngine)}
	if approvalHandler != nil {
		options = append(options, tools.WithApprovalHandler(approvalHandler))
	}
	if emitter != nil {
		options = append(options, tools.WithTelemetryEmitter(emitter))
	}
	registry := tools.NewRegistry(options...)
	if compiled == nil {
		return registry, nil
	}

	if compiled.Has(feature.KindFeature, "filesystem") {
		if err := filesystem.Register(registry, filesystem.Options{
			WorkspaceRoot: cwd,
			Policy:        policyEngine,
			Limits:        tools.ToolLimits{},
		}); err != nil {
			return nil, err
		}
	}
	if compiled.Has(feature.KindFeature, "git") {
		if err := git.Register(registry, git.Options{
			WorkspaceRoot: cwd,
			Policy:        policyEngine,
		}); err != nil {
			return nil, err
		}
	}
	if compiled.Has(feature.KindFeature, "shell") {
		if err := shell.Register(registry, shell.Options{
			WorkspaceRoot: cwd,
			Policy:        policyEngine,
		}); err != nil {
			return nil, err
		}
	}
	return registry, nil
}
