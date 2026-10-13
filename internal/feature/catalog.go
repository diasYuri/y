package feature

// Known returns all capabilities recognized by this build of y. Some may be
// unavailable unless their build tags are present.
func Known() []Descriptor {
	out := make([]Descriptor, len(knownCapabilities))
	copy(out, knownCapabilities)
	sortDescriptors(out)
	return out
}

// IsKnown reports whether a config or registrar refers to a recognized
// capability name.
func IsKnown(kind Kind, id string) bool {
	for _, desc := range knownCapabilities {
		if desc.Kind == kind && desc.ID == id {
			return true
		}
	}
	return false
}

var knownCapabilities = []Descriptor{
	{Kind: KindCommand, ID: "config.validate", Description: "Validate declarative configuration."},
	{Kind: KindCommand, ID: "chat", Description: "Start a basic stdin/stdout chat loop."},
	{Kind: KindCommand, ID: "extension.disable", BuildTag: "feature_wasm_ext", Description: "Disable a WASM extension in runtime config."},
	{Kind: KindCommand, ID: "extension.enable", BuildTag: "feature_wasm_ext", Description: "Enable a WASM extension in runtime config."},
	{Kind: KindCommand, ID: "extension.info", BuildTag: "feature_wasm_ext", Description: "Print metadata about a discovered WASM extension."},
	{Kind: KindCommand, ID: "extension.list", BuildTag: "feature_wasm_ext", Description: "List discovered WASM extensions."},
	{Kind: KindCommand, ID: "extension.validate", BuildTag: "feature_wasm_ext", Description: "Validate a WASM extension manifest."},
	{Kind: KindCommand, ID: "features", Description: "List compiled and unavailable capabilities."},
	{Kind: KindCommand, ID: "doctor", Description: "Run basic environment diagnostics."},
	{Kind: KindCommand, ID: "run", Description: "Execute one prompt headlessly and stream text."},
	{Kind: KindCommand, ID: "session.list", Description: "List saved sessions for the current workspace."},
	{Kind: KindCommand, ID: "session.show", Description: "Print a saved session transcript as JSONL."},

	{Kind: KindFeature, ID: "filesystem", BuildTag: "feature_fs", Description: "Filesystem tools."},
	{Kind: KindFeature, ID: "git", BuildTag: "feature_git", Description: "Git integration."},
	{Kind: KindFeature, ID: "lsp", BuildTag: "feature_lsp", Description: "Language server integration."},
	{Kind: KindFeature, ID: "rpc", BuildTag: "feature_rpc", Description: "RPC/headless mode."},
	{Kind: KindFeature, ID: "shell", BuildTag: "feature_shell", Description: "Subprocess execution."},
	{Kind: KindFeature, ID: "storage_sqlite", BuildTag: "feature_storage_sqlite", Description: "SQLite session storage backend."},
	{Kind: KindFeature, ID: "telemetry", BuildTag: "feature_telemetry", Description: "Telemetry collection."},
	{Kind: KindFeature, ID: "wasm_extensions", BuildTag: "feature_wasm_ext", Description: "WASM extension host."},

	{Kind: KindProvider, ID: "anthropic", BuildTag: "feature_anthropic", Description: "Anthropic provider."},
	{Kind: KindProvider, ID: "google", BuildTag: "feature_google", Description: "Google provider."},
	{Kind: KindProvider, ID: "local", BuildTag: "feature_local", Description: "Local/OpenAI-compatible providers."},
	{Kind: KindProvider, ID: "openai", BuildTag: "feature_openai", Description: "OpenAI provider."},

	{Kind: KindTool, ID: "git_commit", BuildTag: "feature_git", Description: "Create a git commit."},
	{Kind: KindTool, ID: "git_diff", BuildTag: "feature_git", Description: "Read git diff."},
	{Kind: KindTool, ID: "git_show", BuildTag: "feature_git", Description: "Inspect commit metadata and changed paths."},
	{Kind: KindTool, ID: "git_history", BuildTag: "feature_git", Description: "Inspect file and line history."},
	{Kind: KindTool, ID: "git_blame", BuildTag: "feature_git", Description: "Attribute selected lines to commits."},
	{Kind: KindTool, ID: "git_search_history", BuildTag: "feature_git", Description: "Search commits by changed text."},
	{Kind: KindTool, ID: "git_status", BuildTag: "feature_git", Description: "Read git status."},
	{Kind: KindTool, ID: "search_text", BuildTag: "feature_fs", Description: "Search source text with ripgrep."},
	{Kind: KindTool, ID: "search_ast", BuildTag: "feature_fs", Description: "Search code structure with ast-grep."},
	{Kind: KindTool, ID: "outline", BuildTag: "feature_fs", Description: "Return source symbol outlines."},
	{Kind: KindTool, ID: "find_files", BuildTag: "feature_fs", Description: "Discover workspace files."},
	{Kind: KindTool, ID: "definition", BuildTag: "feature_lsp", Description: "Find semantic symbol definitions."},
	{Kind: KindTool, ID: "references", BuildTag: "feature_lsp", Description: "Find semantic symbol references."},
	{Kind: KindTool, ID: "implementations", BuildTag: "feature_lsp", Description: "Find semantic implementations."},
	{Kind: KindTool, ID: "hover", BuildTag: "feature_lsp", Description: "Inspect semantic type and documentation."},
	{Kind: KindTool, ID: "document_symbols", BuildTag: "feature_lsp", Description: "List semantic document symbols."},
	{Kind: KindTool, ID: "callers", BuildTag: "feature_lsp", Description: "Find semantic callers."},
	{Kind: KindTool, ID: "callees", BuildTag: "feature_lsp", Description: "Find semantic callees."},
	{Kind: KindTool, ID: "type_definition", BuildTag: "feature_lsp", Description: "Find semantic type definitions."},
	{Kind: KindTool, ID: "workspace_symbols", BuildTag: "feature_lsp", Description: "Search semantic workspace symbols."},
	{Kind: KindTool, ID: "edit", BuildTag: "feature_fs", Description: "Edit files with audit diffs."},
	{Kind: KindTool, ID: "list_files", BuildTag: "feature_fs", Description: "List files in the workspace."},
	{Kind: KindTool, ID: "patch", BuildTag: "feature_fs", Description: "Apply unified patches with validation."},
	{Kind: KindTool, ID: "read_file", BuildTag: "feature_fs", Description: "Read a file with limits."},
	{Kind: KindTool, ID: "run_command", BuildTag: "feature_shell", Description: "Run a subprocess with limits."},
	{Kind: KindTool, ID: "search", BuildTag: "feature_fs", Description: "Search files in the workspace."},
	{Kind: KindTool, ID: "write_file", BuildTag: "feature_fs", Description: "Write a file with policy checks."},
}
