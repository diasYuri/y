package shell

import core "github.com/yuri/y/pkg/tools"

// Options configures the built-in shell tool.
type Options = core.ShellOptions

// Register adds the shell tool to the supplied registry.
func Register(registry *core.Registry, opts Options) error {
	return core.RegisterShell(registry, opts)
}
