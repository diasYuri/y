package codesearch

import core "github.com/diasYuri/y/pkg/tools"

// Options configures repository-aware source search tools.
type Options = core.CodeSearchOptions

// Register adds search_text, search_ast, outline, and find_files.
func Register(registry *core.Registry, opts Options) error {
	return core.RegisterCodeSearch(registry, opts)
}
