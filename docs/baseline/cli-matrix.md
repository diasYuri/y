# CLI behavior matrix

Activity: `phase-0-baseline-behavior`

Status values:

- `preserve`: keep equivalent behavior in Go.
- `planned-change`: map legacy behavior to the new `y` command shape from the migration spec.
- `gap`: requires a later compatibility or product decision.

## Primary command forms

| Legacy command/form | Current behavior in `pi-mono` | Status | Source in `pi-mono` | Proposed Go destination |
|---|---|---|---|---|
| `pi` | Starts the interactive coding-agent TUI. | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/modes/interactive/interactive-mode.ts` | `cmd/y` and `cmd/y chat` |
| `pi [messages...]` | Starts TUI and submits initial prompt messages after startup. | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/cli/initial-message.ts` | `cmd/y chat [prompt...]` |
| `pi @file [message]` | Reads file/image arguments, builds initial text/image payload and starts the selected mode. | preserve | `packages/coding-agent/src/cli/file-processor.ts`, `packages/coding-agent/src/cli/initial-message.ts`, `packages/coding-agent/src/main.ts` | `cmd/y chat`, `cmd/y run`; streaming file reads and optional image feature |
| `stdin \| pi` | Piped stdin forces non-interactive print mode. | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/modes/print-mode.ts` | `cmd/y run` |
| `pi --print`, `pi -p` | Runs prompt(s), prints final assistant text only, returns non-zero on assistant error/abort. | planned-change | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/modes/print-mode.ts` | `cmd/y run <prompt>` with `--print/-p` as compatibility alias if chosen |
| `pi --mode json` | Non-interactive mode that writes session header and event JSON lines. | preserve | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/modes/print-mode.ts` | `cmd/y run --json` or `cmd/y run --events=jsonl` |
| `pi --mode rpc` | Starts stdin/stdout JSONL RPC server and rejects `@file` args. | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/modes/rpc/rpc-mode.ts`, `packages/coding-agent/src/modes/rpc/rpc-types.ts` | `cmd/y rpc` or `cmd/y chat --rpc`; exact naming is a compatibility decision |
| `pi --help`, `pi -h` | Prints static help plus extension-registered flags after resources load. | preserve | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/main.ts` | `cmd/y help`, Cobra-free or small parser help generator |
| `pi --version`, `pi -v` | Prints package version and exits. | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/config.ts` | `cmd/y --version`, `internal/buildinfo` |

## Session and transcript commands

| Legacy option/command | Current behavior in `pi-mono` | Status | Source in `pi-mono` | Proposed Go destination |
|---|---|---|---|---|
| `--continue`, `-c` | Opens most recent session for the current cwd/session dir. | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/core/session-manager.ts` | `cmd/y session continue` plus compatibility flag |
| `--resume`, `-r` | Opens a TUI session selector over current and all project sessions. | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/cli/session-picker.ts` | `cmd/y session resume`, TUI selector in `pkg/tui` |
| `--session <path|id>` | Opens direct path, local ID prefix, or prompts to fork a global session from another cwd. | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/core/session-manager.ts` | `cmd/y session show/open`, `pkg/coding/session` |
| `--fork <path|id>` | Creates a new session branched from a prior session; conflicts with resume/continue/session/no-session. | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/core/session-manager.ts` | `cmd/y session fork` |
| `--session-dir <dir>` | Overrides settings session storage directory. | preserve | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/core/settings-manager.ts` | `internal/storage/session`, `internal/config` |
| `--no-session` | Uses in-memory session storage and disables persistence. | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/core/session-manager.ts` | `cmd/y run/chat --no-session`, in-memory store |
| `--export <file> [output]` | Exports JSONL session to HTML or specified output and exits. | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/core/export-html/*` | `cmd/y session export`, `pkg/coding/exporthtml` |
| `/export` | Exports current session from interactive mode. | preserve | `packages/coding-agent/src/core/slash-commands.ts`, `packages/coding-agent/src/modes/interactive/interactive-mode.ts` | TUI command in `pkg/coding/commands` |
| `/import` | Imports and resumes a JSONL session. | preserve | `packages/coding-agent/src/core/slash-commands.ts`, `packages/coding-agent/src/core/agent-session-runtime.ts` | `cmd/y session import`, TUI command |
| `/fork`, `/clone`, `/tree`, `/new`, `/resume` | Session branch/tree/new/resume workflows inside TUI. | preserve | `packages/coding-agent/src/core/slash-commands.ts`, `packages/coding-agent/src/core/agent-session.ts`, `packages/coding-agent/src/modes/interactive/interactive-mode.ts` | `pkg/coding/session`, `pkg/tui` selectors |
| `/compact` | Manually compacts the session context. | preserve | `packages/coding-agent/src/core/slash-commands.ts`, `packages/coding-agent/src/core/compaction/*` | `pkg/coding/compaction` |
| `/share` | Shares session as a secret GitHub gist using configured viewer URL. | gap | `packages/coding-agent/src/core/slash-commands.ts`, `packages/coding-agent/src/modes/interactive/interactive-mode.ts`, `packages/coding-agent/src/config.ts` | Needs policy/network decision before Go parity |

## Model, provider and auth options

| Legacy option/command | Current behavior in `pi-mono` | Status | Source in `pi-mono` | Proposed Go destination |
|---|---|---|---|---|
| `--provider <name>` | Selects provider name used with `--model`. | preserve | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/core/model-resolver.ts` | `cmd/y run/chat --provider`; provider resolver |
| `--model <pattern>` | Selects model by exact/fuzzy/glob-ish pattern; supports `provider/model` and `:thinking` suffix. | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/core/model-resolver.ts` | `pkg/coding/models` |
| `--models <patterns>` | Restricts model cycling scope for Ctrl+P and can select initial default. | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/core/model-resolver.ts` | `pkg/coding/models`, TUI model scope selector |
| `--thinking <level>` | Sets reasoning level `off|minimal|low|medium|high|xhigh`; unsupported `xhigh` downgrades later. | preserve | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/main.ts` | `pkg/ai`, `pkg/coding/models` |
| `--api-key <key>` | Runtime-only API key override; requires an explicit model/provider. | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/core/auth-storage.ts` | `internal/auth` runtime override |
| `--list-models [search]` | Lists available authenticated models, optionally filtered. | planned-change | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/cli/list-models.ts` | `cmd/y models list` plus compatibility flag |
| `/model` | Opens model selector. | preserve | `packages/coding-agent/src/core/slash-commands.ts`, `packages/coding-agent/src/modes/interactive/components/model-selector.ts` | `pkg/tui` model selector |
| `/scoped-models` | Enables/disables model cycling scope from TUI. | preserve | `packages/coding-agent/src/core/slash-commands.ts`, `packages/coding-agent/src/modes/interactive/components/scoped-models-selector.ts` | `pkg/tui` scoped model selector |
| `/login`, `/logout` | Configure or remove provider auth from TUI. | planned-change | `packages/coding-agent/src/core/slash-commands.ts`, `packages/coding-agent/src/modes/interactive/components/oauth-selector.ts`, `packages/coding-agent/src/modes/interactive/components/login-dialog.ts` | `cmd/y auth login/logout/list`, TUI compatibility slash commands |
| `pi-ai login [provider]` | OAuth helper CLI with interactive provider selection when omitted. | planned-change | `packages/ai/src/cli.ts`, `packages/ai/src/utils/oauth/index.ts` | Fold into `cmd/y auth login` |
| `pi-ai list` | Lists OAuth providers. | planned-change | `packages/ai/src/cli.ts`, `packages/ai/src/utils/oauth/index.ts` | `cmd/y auth list --oauth` |

## Tool, resource and extension options

| Legacy option/command | Current behavior in `pi-mono` | Status | Source in `pi-mono` | Proposed Go destination |
|---|---|---|---|---|
| `--no-tools`, `-nt` | Disables all built-in and extension/custom tools. | preserve | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/core/sdk.ts` | `internal/config`, `pkg/coding` tool registry |
| `--no-builtin-tools`, `-nbt` | Disables built-in tools while keeping extension/custom tools. | gap | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/main.ts` | Depends on WASM/custom tool support scope |
| `--tools`, `-t <names>` | Allowlist of built-in, extension and custom tool names. | preserve | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/core/agent-session.ts` | `internal/policy`, `pkg/tools` |
| `--extension`, `-e <path>` | Adds explicit TS/JS extension path; may be used multiple times. | planned-change | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/core/resource-loader.ts` | WASM extension path only under `feature_wasm_ext`; no TS runtime |
| `--no-extensions`, `-ne` | Disables extension discovery but keeps explicit extension paths. | planned-change | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/main.ts` | `pkg/extensions/wasm` config |
| `install/remove/uninstall/update/list` | Package manager for installed extension/resource packages, with source management and update behavior. | gap | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/package-manager-cli.ts`, `packages/coding-agent/src/core/package-manager.ts` | Replace with `y extension install/list/validate/...` per WASM spec or document compatibility omission |
| `config` | Opens TUI to enable/disable package resources. | planned-change | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/package-manager-cli.ts`, `packages/coding-agent/src/modes/interactive/components/config-selector.ts` | `cmd/y config validate`, `cmd/y features`, optional TUI settings screen |
| `--skill <path>` / `--no-skills` | Adds or disables skill discovery/loading. | preserve | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/core/resource-loader.ts`, `packages/coding-agent/src/core/skills.ts` | `pkg/coding/resources` |
| `--prompt-template <path>` / `--no-prompt-templates` | Adds or disables prompt templates. | preserve | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/core/prompt-templates.ts`, `packages/coding-agent/src/core/resource-loader.ts` | `pkg/coding/resources` |
| `--theme <path>` / `--no-themes` | Adds or disables theme discovery/loading. | preserve | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/modes/interactive/theme/*` | `pkg/tui/theme` behind `feature_tui` |
| `--no-context-files`, `-nc` | Disables `AGENTS.md` and `CLAUDE.md` discovery/loading. | preserve | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/core/resource-loader.ts` | `pkg/coding/resources` |
| Extension flags | Unknown long flags are preserved as extension flag values and shown in help. | gap | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/core/agent-session-services.ts` | Re-evaluate for WASM V1; likely manifest/config instead of arbitrary CLI flags |

## Runtime and diagnostics options

| Legacy option/env | Current behavior in `pi-mono` | Status | Source in `pi-mono` | Proposed Go destination |
|---|---|---|---|---|
| `--offline` / `PI_OFFLINE=1` | Disables startup network operations and version checks. | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/utils/version-check.ts` | `internal/config`, `internal/diagnostics` |
| `--verbose` | Forces verbose startup and model scope display. | preserve | `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/core/settings-manager.ts` | `internal/log`, `pkg/coding/ui` |
| `PI_CODING_AGENT_DIR` | Overrides default agent config/session/auth directory. | preserve | `packages/coding-agent/src/config.ts`, `packages/coding-agent/src/core/settings-manager.ts` | `internal/paths`, `internal/config` |
| `PI_PACKAGE_DIR` | Overrides shipped asset package directory in legacy Node/Bun packaging. | planned-change | `packages/coding-agent/src/config.ts` | Replace with Go embedded assets or explicit asset dir for dev only |
| `PI_STARTUP_BENCHMARK` | Initializes interactive mode and exits after timing print; only supports interactive mode. | planned-change | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/core/timings.ts` | Replace with `cmd/y profile` / `cmd/y doctor` measurement commands |
| `PI_TELEMETRY` | Controls install telemetry behavior. | gap | `packages/coding-agent/src/core/telemetry.ts`, `packages/coding-agent/src/modes/interactive/interactive-mode.ts` | Decide telemetry policy in diagnostics/release docs |

## Target command reconciliation

| Spec command | Legacy equivalent | Status | Source in `pi-mono` | Proposed Go destination |
|---|---|---|---|---|
| `y` | `pi` | preserve | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/modes/interactive/interactive-mode.ts` | `cmd/y` |
| `y run <prompt>` | `pi --print <prompt>` and piped stdin | planned-change | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/modes/print-mode.ts` | `cmd/y run`, `pkg/coding/headless` |
| `y chat` | `pi` interactive | planned-change | `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/modes/interactive/interactive-mode.ts` | `cmd/y chat`, `pkg/coding/ui` |
| `y auth login/list/logout` | `/login`, `/logout`, `pi-ai login/list` | planned-change | `packages/coding-agent/src/core/slash-commands.ts`, `packages/ai/src/cli.ts` | `cmd/y auth`, `internal/auth` |
| `y config validate` | Partial legacy settings parsing only | planned-change | `packages/coding-agent/src/core/settings-manager.ts`, `packages/coding-agent/src/package-manager-cli.ts` | `cmd/y config validate`, `internal/config` |
| `y features` | No direct legacy equivalent | planned-change | `packages/coding-agent/src/core/tools/index.ts`, `packages/ai/src/providers/register-builtins.ts` | `cmd/y features`, `internal/feature` |
| `y doctor` | No direct legacy equivalent | planned-change | `packages/coding-agent/src/core/diagnostics.ts`, `packages/coding-agent/src/core/timings.ts` | `cmd/y doctor`, `internal/diagnostics` |
| `y models list` | `pi --list-models` | planned-change | `packages/coding-agent/src/cli/list-models.ts`, `packages/coding-agent/src/core/model-registry.ts` | `cmd/y models list`, `pkg/ai/models` |
| `y session list/show` | `--resume`, selectors, `/session` | planned-change | `packages/coding-agent/src/core/session-manager.ts`, `packages/coding-agent/src/modes/interactive/components/session-selector.ts` | `cmd/y session`, `internal/storage/session` |
