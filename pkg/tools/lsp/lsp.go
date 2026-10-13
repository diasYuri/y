package lsp

import core "github.com/diasYuri/y/pkg/tools"

// Options configures semantic language-server tools.
type Options = core.LSPOptions

// Register adds semantic navigation tools to a registry.
func Register(registry *core.Registry, opts Options) error {
	return core.RegisterLSP(registry, opts)
}
