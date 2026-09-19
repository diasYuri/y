package git

import core "github.com/diasYuri/y/pkg/tools"

// Options configures the built-in git tools.
type Options = core.GitOptions

// Register adds git tools to the supplied registry.
func Register(registry *core.Registry, opts Options) error {
	return core.RegisterGit(registry, opts)
}
