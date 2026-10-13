# Codebase navigation tools

The coding agent's navigation tools follow this sequence:

```text
discover → narrow → inspect structure → resolve semantics → inspect code → inspect history → verify
```

`search_text` discovers textual candidates, `outline` and `search_ast` describe syntax, LSP resolves symbols and relationships, Git tools explain changes, and `read_file` retrieves the smallest useful source range.

## Tools and binaries

| Tool | Use | Binary or feature |
| --- | --- | --- |
| `search_text` | Default textual search with structured paths, lines, columns, and optional context | `rg` required; missing binary returns an explicit error |
| `search_ast` | Match code by syntactic shape | Optional `ast-grep` or `sg` |
| `outline` | List symbols and ranges in a source file or directory | Optional ast-grep version with the `outline` command and JSON output |
| `find_files` | Discover repository or filesystem entries | Uses `rg --files`; has a filesystem walker fallback |
| `search` | Compatibility literal or regular-expression search | Built in; prefer `search_text` when ripgrep is available |
| LSP tools | Resolve symbol definitions, references, implementations, types, callers, and callees | `feature_lsp` build feature plus a language server |
| Git tools | Inspect status, staged or unstaged diffs, commits, line attribution, and history | Git required; history tools report Git errors directly |

Text search respects `.gitignore` by default and skips binary files. `search_text` supports literal or regex matching, case options, whole words, globs, language filters, bounded result counts, context lines, and structured results. It invokes `rg` with argv and does not parse terminal-colored output.

`find_files` uses repository-aware listing by default. Set `include_ignored` when searching build output or ignored files. Use `type: "directory"` or `include_ignored: true` for filesystem-walker semantics, including untracked artifacts.

## LSP support

Language is detected from the source extension. The project root is selected from the nearest language marker under the workspace, such as `go.mod`, `Cargo.toml`, `package.json`, `pyproject.toml`, `compile_commands.json`, `.csproj`, or `pom.xml`.

Built-in server commands are:

| Language | Server command |
| --- | --- |
| Go | `gopls` |
| Rust | `rust-analyzer` |
| TypeScript / JavaScript | `typescript-language-server --stdio` |
| Python | `basedpyright-langserver --stdio`, falling back to `pyright-langserver --stdio` |
| C / C++ | `clangd` |
| C# | `csharp-ls`, falling back to `Microsoft.CodeAnalysis.LanguageServer` |
| Java | `jdtls` |

The manager maintains one server process per workspace root and language, sends standard initialize/initialized lifecycle messages, and reuses the process for navigation calls. It synchronizes the latest on-disk document with `didOpen` or `didChange` before a request. Files over 8 MiB are not sent to a server. A crashed process is detected and restarted on the next request. Closing the registry shuts down its server processes. Unix servers run in their own process group so shutdown can terminate child processes too.

LSP integration is compiled with the `feature_lsp` build tag (included in the full build flavor). When the feature is absent, tools return an `lsp_unavailable` error and recommend AST/text search and targeted reads. When the integration is built but a server is missing, the error names the language and command.

Custom commands can be provided at registration time:

```go
err := lsp.Register(registry, lsp.Options{
    WorkspaceRoot: workspace,
    Servers: []coreLSP.ServerConfig{
        {Language: "python", Name: "pyright", Command: "/opt/pyright-langserver", Args: []string{"--stdio"}},
    },
})
```

`coreLSP` above is `github.com/diasYuri/y/pkg/lsp`. Commands and arguments are passed directly to `exec.Command`; they are not interpreted by a shell. The registry applies its process-execution policy before each server start or restart. Sensitive-operation approval is required by default. Set `policy.Config.DisableSensitiveApproval` (JSON: `disable_sensitive_approval`) to opt out; `RequireApprovalForSensitive` takes precedence if both are set.

Tool inputs use 1-based lines and columns. LSP positions are converted to and from UTF-16 internally; text and AST columns are returned as 1-based Unicode character columns.

## Git investigation

`git_status` reports branch-aware status. `git_diff` supports staged diffs with `cached: true`. `git_log` remains available as the compatibility text view. `git_show` returns commit metadata and changed paths without a patch by default. `git_history` supports file or line history, `git_blame` supports a selected line range, and `git_search_history` supports `mode: "string"` (`git log -S`) or `mode: "regex"` (`git log -G`). Results are bounded by commit, line, and output limits.

## Navigation examples

Find a candidate, resolve its exact definition and callers, then read only the surrounding range:

```text
search_text(query="Authorize", path="internal/payment", language="go")
→ definition(path="internal/payment/service.go", line=143, column=18)
→ callers(path="internal/payment/service.go", line=143, column=18)
→ read_file(path="internal/payment/service.go", start_line=130, end_line=185)
```

Inspect a source file's public structure, find a syntactic pattern, then inspect why the selected lines changed:

```text
search_text(query="retry", glob="*.go")
→ outline(path="internal/payment/service.go", include_private=true)
→ search_ast(pattern="go($CALL)", language="go", path="internal/payment")
→ git_blame(path="internal/payment/service.go", start_line=143, end_line=160)
→ git_show(commit="<commit from blame>")
```

## Fallbacks and troubleshooting

- If `rg` is missing, install ripgrep or use compatibility `search`. The agent should treat text results as candidates, not semantic identity.
- If ast-grep is missing or its installed version has no `outline` command, use LSP and text search; the tool reports the missing capability explicitly.
- If a language server is missing, install its command or override the server configuration. The agent can continue with AST search, textual search, and targeted reads without claiming semantic certainty.
- If a server fails to initialize, check that its command supports stdio LSP, that its project dependencies are available, and that the workspace root contains the expected project manifest. The tool includes bounded server stderr in initialization errors.
- If Git history is unavailable because the directory is not a Git repository or has no commits, skip historical analysis and inspect current source and tests.
- Narrow paths, globs, language filters, and line ranges when a result reaches its limit. Do not increase limits before narrowing the question.
