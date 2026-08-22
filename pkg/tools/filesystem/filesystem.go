package filesystem

import core "github.com/yuri/y/pkg/tools"

// Options configures the built-in filesystem tools.
type Options = core.FilesystemOptions

// Register adds filesystem tools to the supplied registry.
func Register(registry *core.Registry, opts Options) error {
	return core.RegisterFilesystem(registry, opts)
}
