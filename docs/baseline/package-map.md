# Package map: TypeScript to Go

Activity: `phase-0-baseline-inventory`

This map records where each existing TypeScript package and major source area should land in the Go migration. It is an implementation planning artifact, not an API compatibility promise.

## Package-level mapping

| Current TypeScript package | Current role | Go destination | Build-tag / product notes |
|---|---|---|---|
| `packages/ai` | Provider registry, model registry, streaming, OAuth helper CLI | `pkg/ai`, `pkg/providers`, `internal/auth`, `cmd/y` auth/model subcommands | Providers behind tags such as `feature_openai`, `feature_anthropic`, `feature_google`, `feature_local`; OAuth should be optional by provider |
| `packages/agent` | Agent loop and stateful agent wrapper | `pkg/agent` | Core package, no provider-specific imports |
| `packages/coding-agent` | Main CLI/TUI, sessions, tools, settings, extensions, package resources | `cmd/y`, `pkg/coding`, `pkg/tools`, `internal/config`, `internal/storage`, `internal/policy`, `pkg/extensions` | Main product. Runtime must not require Node/Bun |
| `packages/tui` | Terminal rendering/input toolkit | `pkg/tui` | Behind `feature_tui`; keep independent enough for golden tests |
| `packages/mom` | Slack bot product | `cmd/y-mom`, `pkg/mom` | Behind `feature_mom`; Slack dependencies isolated from main `y-minimal` |
| `packages/pods` | GPU pod/vLLM CLI | `cmd/y-pods`, `pkg/pods` | Behind `feature_pods`; SSH subprocesses must have context/timeouts/streaming limits |
| `packages/web-ui` | Browser components and artifact renderers | `cmd/y-web`, `pkg/web`, static asset directory | Behind `feature_web`; JS can remain build artifact only, not `y` runtime dependency |
| `packages/web-ui/example` | Vite demo | `testdata/web-ui-example` or removed from runtime tree | Build/test artifact only |

## Detailed source-area mapping

### `packages/ai`

| TypeScript source | Behavior | Go destination | Migration notes |
|---|---|---|---|
| `src/types.ts` | Message, content, tool call, usage, model, stream option types | `pkg/ai` | Use typed structs and `json.RawMessage` for provider-specific options; avoid `map[string]any` on hot paths |
| `src/utils/event-stream.ts`, `src/stream.ts` | Assistant stream protocol and helpers | `pkg/ai/stream` or `pkg/ai` | Implement pull-style `EventStream.Next(ctx)` plus adapters for channel/event emission |
| `src/api-registry.ts` | API stream function registry | `pkg/providers` registry | Register compiled providers explicitly from bootstrap; no global side effects needed |
| `src/providers/register-builtins.ts` | Lazy provider module registration | `pkg/providers/register.go` with build-tag files | Replace lazy JS imports with build tags and explicit registration |
| `src/providers/openai-responses*.ts` | OpenAI Responses and shared parsing | `pkg/providers/openai` | First provider candidate; stream SSE without buffering full response |
| `src/providers/openai-completions.ts` | OpenAI-compatible chat completions | `pkg/providers/openai`, `pkg/providers/compatible` | Covers OpenRouter, DeepSeek, Groq, xAI, Cerebras, Cloudflare, etc. via model compat rules |
| `src/providers/openai-codex-responses.ts` | OpenAI Codex Responses variant | `pkg/providers/openai` or `pkg/providers/codex` | Decide if distinct provider type is required |
| `src/providers/anthropic.ts` | Anthropic Messages SSE and thinking/tool quirks | `pkg/providers/anthropic` | Preserve thinking, eager tool input and cache behavior tests |
| `src/providers/google*.ts`, `google-shared.ts` | Gemini, Gemini CLI, Antigravity, Vertex | `pkg/providers/google` | Split auth/transport variants under one package with build tags where useful |
| `src/providers/amazon-bedrock.ts`, `bedrock-provider.*` | Bedrock converse stream | `pkg/providers/bedrock` | Optional feature; AWS signing/auth has non-trivial dependency and memory impact |
| `src/providers/mistral.ts` | Mistral conversations | `pkg/providers/mistral` | Optional provider after core matrix |
| `src/providers/faux.ts` | Fake provider for tests | `pkg/providers/fake` | Keep as first-class test helper |
| `src/providers/transform-messages.ts` | Cross-provider message normalization | `pkg/providers/transform` or `pkg/ai/convert` | Important for handoff/replay compatibility |
| `src/models.ts`, `src/models.generated.ts` | Static model registry and cost metadata | `pkg/ai/models`, generated Go data | Generate Go source or embed JSON with typed decode at startup |
| `src/env-api-keys.ts` | Env var API key lookup | `internal/auth` or `pkg/providers/auth` | Centralize secret lookup and redaction |
| `src/utils/oauth/*`, `src/oauth.ts`, `src/cli.ts` | OAuth provider login flows and `pi-ai` helper | `internal/auth/oauth`, `cmd/y auth login/list/logout` | `pi-ai` should collapse into `y auth ...` unless kept as dev helper |
| `src/utils/{overflow,validation,json-parse,sanitize-unicode,headers,hash}.ts` | Provider support utilities | `pkg/ai/internal` or `internal/providerutil` | Keep tests provider-focused |

### `packages/agent`

| TypeScript source | Behavior | Go destination | Migration notes |
|---|---|---|---|
| `src/types.ts` | Agent messages, events, tool interfaces, state types | `pkg/agent` | Keep provider-neutral types; align with `pkg/ai` event model |
| `src/agent-loop.ts` | Stateless loop, turn lifecycle, tool execution, steering/follow-up queues | `pkg/agent/loop.go` | Use `context.Context`; bounded parallel tool execution from config |
| `src/agent.ts` | Stateful wrapper, subscribers, queue modes, abort handling | `pkg/agent/session.go` or `pkg/agent/agent.go` | Preserve queue modes `all` and `one-at-a-time` |
| `src/proxy.ts` | Proxy/helpers for agent events | `pkg/agent` or remove after inspection | Needs exact inspection in behavior matrix phase |
| `test/*` | Loop regression tests | `pkg/agent` tests | Port as unit tests with fake provider/tool streams |

### `packages/coding-agent`

| TypeScript source | Behavior | Go destination | Migration notes |
|---|---|---|---|
| `src/cli.ts`, `src/main.ts`, `src/cli/args.ts` | CLI bootstrap, arg parsing, help, modes | `cmd/y`, `internal/app`, `internal/cli` | Replace current flat flags with spec commands while preserving legacy behavior decisions in behavior matrix |
| `src/cli/file-processor.ts`, `initial-message.ts` | `@file` and stdin prompt preparation, image handling | `pkg/coding/input` | Stream file reads; image resizing is a separate optional feature |
| `src/cli/list-models.ts`, `model-resolver.ts`, `model-registry.ts` | Model selection/listing/cycling | `pkg/coding/models`, `pkg/ai/models` | Needs deterministic fuzzy/glob matching tests |
| `src/config.ts` | Package/config path resolution and install method detection | `internal/config`, `internal/paths`, `internal/buildinfo` | Remove Node/Bun install detection from main runtime; keep package asset path logic for Go binaries |
| `src/core/settings-manager.ts` | Global/project JSON settings with locks | `internal/config`, `internal/storage` | Spec target is TOML; migrate with JSON import or compatibility decision |
| `src/core/auth-storage.ts`, `auth-guidance.ts` | Auth file and user guidance | `internal/auth` | Enforce file permissions and secret redaction |
| `src/core/session-manager.ts`, `messages.ts`, `migrations.ts` | Session storage, tree entries, migrations | `internal/storage/session`, `pkg/coding/session` | Decide whether Go reads legacy session JSONL v1-v3 |
| `src/core/agent-session*.ts`, `sdk.ts` | High-level session runtime and services | `pkg/coding` | Compose agent, providers, tools, storage and UI adapters |
| `src/core/tools/*.ts` | Built-in tools and schemas | `pkg/tools`, `internal/policy` | Implement `read_file`, `write_file`, `list_files`, `search`, `edit`, `run_command`; map names carefully if CLI keeps legacy aliases |
| `src/core/bash-executor.ts`, `exec.ts` | Streaming subprocess execution | `pkg/tools/shell` | Mandatory context cancel, timeout and stdout/stderr byte limits |
| `src/core/compaction/*` | Context compaction and branch summaries | `pkg/coding/compaction` | Requires provider/token estimation decision |
| `src/core/extensions/*` | TS extension loader/runtime/types | `pkg/extensions/wasm` | Do not port TS dynamic loading into runtime; use as requirements source for WASM capabilities |
| `src/core/package-manager.ts`, `src/package-manager-cli.ts` | npm/git/local package resources | `pkg/extensions`, `internal/packages` or defer | Likely replaced by WASM extension install/validate commands |
| `src/core/skills.ts`, `prompt-templates.ts`, `resource-loader.ts`, `slash-commands.ts` | Resource discovery and commands | `pkg/coding/resources` | Keep local file resource loading without Node |
| `src/core/export-html/*` | Static transcript export | `pkg/coding/exporthtml` | Use embedded templates; avoid browser build dependency |
| `src/modes/interactive/*` | TUI mode and components | `pkg/tui`, `pkg/coding/ui` | Separate generic TUI toolkit from coding-agent-specific components |
| `src/modes/rpc/*` | RPC JSONL mode/client/types | `pkg/coding/rpc`, `cmd/y run/chat` | Spec names differ; decide compatibility |
| `src/modes/print-mode.ts` | Headless print mode | `pkg/coding/headless`, `cmd/y run` | Must not initialize TUI |
| `src/utils/*` | Git, paths, shell, image, clipboard, watchers | `internal/*`, `pkg/tools`, optional feature packages | Split by capability/build tag |
| `src/bun/*` | Bun binary entry and env restore | No main Go target | Only informs release/cutover docs |
| `docs/`, `examples/` | User docs and extension examples | `docs/`, `examples/wasm` | Rewrite for Go and WASM extension model |

### `packages/tui`

| TypeScript source | Behavior | Go destination | Migration notes |
|---|---|---|---|
| `src/tui.ts`, `src/terminal.ts`, `src/stdin-buffer.ts` | Terminal state, rendering, input buffer | `pkg/tui` | Build differential renderer with reusable buffers |
| `src/keybindings.ts`, `src/keys.ts` | Key IDs and configurable bindings | `pkg/tui/key` or `pkg/tui` | No hardcoded call-site bindings in Go |
| `src/editor-component.ts`, `src/components/editor.ts`, `input.ts`, `kill-ring.ts`, `undo-stack.ts` | Prompt editor behavior | `pkg/tui/editor` | Port key behavior with golden/input tests |
| `src/components/*` | UI components | `pkg/tui/components` or internal subpackage | Keep small abstractions; avoid over-frameworking |
| `src/terminal-image.ts`, `components/image.ts` | Terminal image display | Optional `feature_tui_images` or TUI subfeature | Determine support matrix by terminal |
| `src/fuzzy.ts`, `autocomplete.ts` | Fuzzy matching and autocomplete | `pkg/tui/autocomplete` or `internal/fuzzy` | Useful for model/session selectors |
| `test/*` | Renderer, key, editor and regression tests | Go golden/input tests | Use as parity source |

### `packages/mom`

| TypeScript source | Behavior | Go destination | Migration notes |
|---|---|---|---|
| `src/main.ts` | CLI, Slack bot bootstrap, per-channel state | `cmd/y-mom`, `pkg/mom` | Isolate behind `feature_mom` |
| `src/slack.ts`, `events.ts`, `download.ts` | Slack Socket Mode/Web API integration | `pkg/mom/slack` | Use Go Slack library or direct HTTP/WebSocket after dependency review |
| `src/agent.ts`, `context.ts` | Agent runner and Slack context adapter | `pkg/mom/agent` | Reuse `pkg/coding` session runtime where possible |
| `src/sandbox.ts` | Host/docker sandbox selection | `pkg/mom/sandbox`, `internal/policy` | Subprocess/docker use requires explicit policy |
| `src/tools/*` | Slack-specific tools | `pkg/mom/tools` | Compare with core tools before duplicating |
| `src/store.ts`, `log.ts`, `fs-watch.ts` | Persistence/log/watch | `internal/storage`, `pkg/mom` | Need storage limits and log redaction |

### `packages/pods`

| TypeScript source | Behavior | Go destination | Migration notes |
|---|---|---|---|
| `src/cli.ts` | Pod CLI command parser | `cmd/y-pods` | Consider subcommand framework only if it does not bloat minimal build |
| `src/config.ts`, `types.ts` | Local pod config | `pkg/pods`, `internal/config` | Use atomic writes and explicit config path |
| `src/ssh.ts` | SSH command execution/streaming | `pkg/pods/ssh` or `pkg/tools/shell` | Prefer external `ssh` subprocess with timeout/stream limits |
| `src/commands/pods.ts` | Pod setup/list/active/remove | `pkg/pods` | Preserve local config semantics |
| `src/commands/models.ts`, `model-configs.ts`, `models.json` | vLLM model lifecycle | `pkg/pods/models`, embedded data | Copy `models.json` into Go embed or generated Go |
| `src/commands/prompt.ts` | Agent prompt against pod endpoint | `pkg/pods/agent` | Reuse OpenAI-compatible provider |
| `scripts/` | Remote setup scripts | `pkg/pods/scripts` embedded assets | Shell scripts remain external to main runtime |

### `packages/web-ui`

| TypeScript source | Behavior | Go destination | Migration notes |
|---|---|---|---|
| `src/ChatPanel.ts`, `src/components/*`, `src/dialogs/*` | Browser chat UI | Static web asset or server-rendered Go UI | Spec recommends static artifact for initial migration |
| `src/storage/*` | IndexedDB/app storage | Browser asset only | Not part of Go CLI runtime |
| `src/tools/artifacts/*`, `src/tools/renderers/*` | Artifact renderers for documents/images/HTML/SVG/PDF/XLSX | Browser asset only or Go server endpoints | Heavy document libraries should not enter main `y` |
| `src/components/sandbox/*` | Browser runtime bridge | Browser asset only | Requires separate web behavior matrix |
| `src/utils/model-discovery.ts`, provider settings components | Browser provider UX | `cmd/y-web` API or static-only behavior | Needs product decision |
| `example/*` | Demo app | Test/demo artifact | Keep outside release runtime |

## Current dependency fate in Go

| Current dependency group | Go migration stance |
|---|---|
| Node/Bun/TypeScript runtime | Remove from main runtime entirely |
| Provider SDK packages | Prefer direct HTTP/SSE Go implementations for core providers; only add Go SDKs if they do not bloat or obscure streaming |
| `typebox`, schema conversion | Replace with Go JSON Schema descriptors and typed request structs |
| `partial-json` | Replace with bounded incremental JSON parser/assembler for streamed tool calls |
| `undici`, proxy-agent | Replace with Go `net/http` transport and proxy settings |
| `chalk`, `strip-ansi`, `marked`, terminal width libs | Replace with Go terminal rendering/ANSI/Markdown utilities chosen for footprint |
| `diff`, `glob`, `ignore`, `minimatch` | Replace with Go libraries or stdlib implementations; preserve gitignore/glob semantics explicitly |
| `proper-lockfile`, `uuid`, `yaml` | Replace with Go file locks/atomic writes, UUID package if needed, TOML config per spec |
| `@silvia-odwyer/photon-node`, clipboard optional dep | Optional image/clipboard features, isolated behind build tags or omitted initially |
| Slack SDKs | Optional `y-mom` dependency only |
| Browser document libraries | Static web artifact only, not linked into main Go binary |

## Naming reconciliation

The migration spec names target binaries `y`, `y-mom`, `y-pods`, `y-web`. The legacy package names and help text still use `pi`, `mom` and `pi-pods`.

Planned mapping:

| Legacy | Go target |
|---|---|
| `pi` | `y` |
| `mom` | `y-mom` |
| `pi-pods` / package `@mariozechner/pi` | `y-pods` |
| `pi-ai` | Fold into `y auth` and `y models` commands unless a separate helper is explicitly retained |
| `pi-web-ui` | `y-web` static/server UI artifact |

## Inspection gaps

See `docs/baseline/gaps.md` for the follow-up list. Items that may change this map:

- Whether legacy `pi` flags become compatibility aliases for `y`.
- Whether legacy JSON settings and sessions must be imported automatically.
- Whether `y-web` is retained in initial cutover.
- Whether extension package management is replaced completely by WASM extension install/validate flows.
