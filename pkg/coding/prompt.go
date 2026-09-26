package coding

import "strings"

// SystemPrompt returns the default system prompt used by NewAgent. It is
// optimized for coding tasks and lists the built-in tool families installed
// by the quickstart (filesystem, shell, git).
func SystemPrompt() string {
	return systemPromptForToolFamilies(nil)
}

func systemPromptForToolFamilies(families []string) string {
	families = quickstartToolFamilies(families)
	var prompt strings.Builder
	prompt.WriteString(`You are a senior software engineer AI assistant. You help with code analysis, refactoring, testing, and implementation.

Available tools:
`)

	for _, family := range families {
		switch family {
		case "filesystem":
			prompt.WriteString("- read_file: Read a file's contents\n")
			prompt.WriteString("- write_file: Write or overwrite a file\n")
			prompt.WriteString("- list_files: List files in a directory\n")
			prompt.WriteString("- search: Search for text patterns in files\n")
			prompt.WriteString("- edit: Edit a file with old/new text replacement\n")
			prompt.WriteString("- patch: Apply a unified diff patch\n")
		case "shell":
			prompt.WriteString("- run_command: Execute a shell command (tests, builds, git, etc.)\n")
		case "git":
			prompt.WriteString("- git_status: Check git status\n")
			prompt.WriteString("- git_diff: View git diff\n")
			prompt.WriteString("- git_log: View commit history\n")
			prompt.WriteString("- git_branch: List or inspect branches\n")
			prompt.WriteString("- git_checkout: Switch branches\n")
			prompt.WriteString("- git_commit: Create a git commit\n")
		}
	}

	prompt.WriteString(`
When working on tasks:
1. First understand the codebase using the available tools
2. Use the available tools to inspect, change, and verify the workspace
3. Always verify your changes compile and tests pass

Keep responses concise and focused. When writing code, follow existing style and conventions in the project.`)
	return prompt.String()
}
