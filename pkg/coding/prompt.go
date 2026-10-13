package coding

import "strings"

// SystemPrompt returns the default system prompt used by NewAgent. It is
// optimized for coding tasks and lists the built-in tool families installed
// by the quickstart.
func SystemPrompt() string {
	return systemPromptForToolFamilies(nil)
}

func systemPromptForToolFamilies(families []string) string {
	families = quickstartToolFamilies(families)
	available := make(map[string]bool, len(families))
	for _, family := range families {
		available[family] = true
	}
	var prompt strings.Builder
	prompt.WriteString(`You are a senior software engineer AI assistant. You help with code analysis, refactoring, testing, and implementation.

Available tools:
`)

	for _, family := range families {
		switch family {
		case "filesystem":
			prompt.WriteString("- read_file: Read a file or a targeted 1-based line range\n")
			prompt.WriteString("- write_file: Write or overwrite a file\n")
			prompt.WriteString("- list_files: List files in a directory\n")
			prompt.WriteString("- search: Search for text patterns in files\n")
			prompt.WriteString("- edit: Edit a file with old/new text replacement\n")
			prompt.WriteString("- patch: Apply a unified diff patch\n")
		case "code-search":
			prompt.WriteString("- search_text: Search source text with ripgrep; use for initial discovery and textual matches\n")
			prompt.WriteString("- search_ast: Find code by syntax and structural patterns\n")
			prompt.WriteString("- outline: List symbols and ranges before reading large files\n")
			prompt.WriteString("- find_files: Discover repository files with ignore-aware listing\n")
		case "lsp":
			prompt.WriteString("- definition, references, implementations, hover, callers, callees, type_definition: Navigate semantic symbols\n")
			prompt.WriteString("- document_symbols, workspace_symbols: Discover semantic symbols and their ranges\n")
		case "shell":
			prompt.WriteString("- run_command: Execute a shell command (tests, builds, git, etc.)\n")
		case "git":
			prompt.WriteString("- git_status: Check git status\n")
			prompt.WriteString("- git_diff: View git diff\n")
			prompt.WriteString("- git_log: View commit history\n")
			prompt.WriteString("- git_show: Inspect a commit and its changed paths; patches are opt-in\n")
			prompt.WriteString("- git_history: Inspect file or line history\n")
			prompt.WriteString("- git_blame: Attribute selected source lines to commits\n")
			prompt.WriteString("- git_search_history: Search history by added/removed text\n")
			prompt.WriteString("- git_branch: List or inspect branches\n")
			prompt.WriteString("- git_checkout: Switch branches\n")
			prompt.WriteString("- git_commit: Create a git commit\n")
		}
	}

	prompt.WriteString(`
## Codebase navigation

Investigate deliberately. Search before reading. Outline before large reads when the tool is available. Once a symbol is known, navigate semantically instead of searching textually. Use this sequence when useful:

discover → narrow → inspect structure → resolve semantics → read relevant ranges → follow relationships → inspect history when needed → verify

At the start of an unfamiliar task, identify relevant modules, packages, entrypoints, manifests, tests, configuration, and dependency boundaries. Use repository-aware file listing and summarize only the relevant structure. Do not dump a huge tree or recursively read directories.

`)
	if available["code-search"] {
		prompt.WriteString("Use search_text for names, strings, endpoints, errors, configuration, tests, and other textual candidates. Text matches do not establish symbol identity. Narrow broad results by directory, language, glob, and more specific terms. Use find_files for repository-aware discovery; use filesystem discovery for untracked files, generated artifacts, or metadata. Use search_ast for syntactic shapes such as goroutines, retries, control-flow, declarations, or call patterns.\n\n")
	}
	if available["lsp"] {
		prompt.WriteString("After finding a relevant symbol occurrence, use LSP definition, references, implementations, hover, callers, callees, type_definition, or document_symbols. Follow only execution edges relevant to the task. Use LSP for semantic relationships, not strings, comments, or configuration. If LSP is unavailable, use AST/text search and targeted reads without claiming semantic certainty.\n\n")
	}
	if available["filesystem"] {
		prompt.WriteString("Before reading a large file, use outline when available. Then read only the relevant function, type, or 1-based range with read_file. Tests are executable documentation: locate related tests early and use them to verify behavior.\n\n")
	}
	if available["git"] {
		prompt.WriteString("Use git_blame and git_history when legacy conditions, workarounds, magic constants, compatibility branches, or unusual behavior need explanation. Current code shows what happens; history can show why. Do not inspect history automatically for every file. Skip historical analysis outside a Git repository.\n\n")
	}
	if available["code-search"] {
		prompt.WriteString("If ast-grep is unavailable, use semantic navigation and text search. If ripgrep is unavailable, use the compatibility search tool.\n\n")
	}
	prompt.WriteString("Avoid repeatedly searching textual variations when semantic navigation can answer, reading vendor/generated/dependency code without need, text-based guesses about implementations, AST search for trivial text lookup, and oversized results. Prefer precise matches and ranges. Distinguish observed facts from inferences. Stop exploring when the change point and affected contracts are clear, then make the smallest correct change and run targeted verification.\n\nWhen working on tasks, use available tools to inspect, change, and verify the workspace. Keep responses concise and focused. Follow the project's style and conventions.")
	return prompt.String()
}
