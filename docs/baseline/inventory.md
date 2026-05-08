# Baseline inventory: pi-mono

Activity: `phase-0-baseline-inventory`

Source inspected: `pi-mono` at workspace root. Destination for this artifact: `y/docs/baseline`.

## Repository shape

`pi-mono` is a Node/TypeScript npm workspace with seven direct packages under `packages/` and one nested example workspace:

| Path | Package | Product role | Current runtime surface |
|---|---|---|---|
| `packages/agent` | `@mariozechner/pi-agent-core` | Agent loop library | TypeScript library consumed by CLI, Slack bot, pods and tests |
| `packages/ai` | `@mariozechner/pi-ai` | AI provider and streaming library | TypeScript library plus `pi-ai` OAuth helper CLI |
| `packages/coding-agent` | `@mariozechner/pi-coding-agent` | Main coding agent product | `pi` CLI/TUI, RPC/print modes, sessions, tools, extensions, package manager |
| `packages/mom` | `@mariozechner/pi-mom` | Slack automation product | `mom` CLI / Slack Socket Mode bot |
| `packages/pods` | `@mariozechner/pi` | GPU pod/vLLM management product | `pi-pods` CLI |
| `packages/tui` | `@mariozechner/pi-tui` | Terminal UI toolkit | TypeScript terminal renderer/input library |
| `packages/web-ui` | `@mariozechner/pi-web-ui` | Browser UI components | Static browser JS/CSS package |
| `packages/web-ui/example` | `pi-web-ui-example` | Local demo app | Vite example for `pi-web-ui` |

Top-level workspace scripts:

| Script | Purpose | Dependency phase |
|---|---|---|
| `clean` | Run workspace cleans | Build-time |
| `build` | Build TUI, AI, agent, coding-agent, mom, web-ui and pods in order | Build-time |
| `dev` | Concurrent watch builds for core packages | Build-time |
| `dev:tsc` | Watch AI and web-ui type/build steps | Build-time |
| `check` | Biome format/check, `tsgo --noEmit`, browser smoke, web-ui check | Test-time |
| `check:browser-smoke` | Browser smoke script | Test-time |
| `profile:tui` | Node memory/profile harness for coding-agent TUI | Measurement-time |
| `profile:rpc` | Node memory/profile harness for coding-agent RPC | Measurement-time |
| `test` | Run workspace tests where present | Test-time |
| `version:*` | npm workspace version management | Release-time |
| `prepublishOnly`, `publish`, `publish:dry`, `release:*` | Package release flow | Release-time |
| `prepare` | Husky install | Build/dev-time |

## Products and commands

| Product | Current command | Current implementation | Main flows |
|---|---|---|---|
| Coding agent | `pi` | `packages/coding-agent/dist/cli.js`; optional Bun compiled binary at `dist/pi` | Interactive TUI, print mode, JSON output, RPC mode, session continue/resume/fork, HTML export, model listing, extension/package management, config TUI |
| AI helper | `pi-ai` | `packages/ai/dist/cli.js` | OAuth login and provider listing |
| Slack bot | `mom` | `packages/mom/dist/main.js` | Slack Socket Mode bot for channels, optional download mode, host/docker sandbox selection |
| Pods | `pi-pods` | `packages/pods/dist/cli.js` | Pod setup/list/active/remove, SSH/shell, vLLM model start/stop/list/logs, agent against pod endpoint |
| Web UI | npm module | `packages/web-ui/dist/index.js`, `dist/app.css` | Browser chat components, storage, provider settings, artifact renderers |

### `pi` CLI surface

Primary invocation forms:

| Form | Behavior |
|---|---|
| `pi` | Interactive TUI |
| `pi [messages...]` | Interactive TUI with initial prompt |
| `pi @file [message]` | Adds file or image content to initial message |
| `pi -p/--print [message]` | Non-interactive text output |
| `pi --mode json` | Non-interactive JSON-oriented output |
| `pi --mode rpc` | JSONL/RPC mode |
| `pi --continue` | Continue previous session |
| `pi --resume` | Select a session to resume |
| `pi --session <path|id>` | Open a specific session |
| `pi --fork <path|id>` | Fork a session |
| `pi --export <file> [output.html]` | Export session to HTML |
| `pi --list-models [search]` | List available models |
| `pi install/remove/uninstall/update/list` | Manage installed extension/resource packages |
| `pi config` | Open TUI to enable/disable package resources |

Important flags: `--provider`, `--model`, `--api-key`, `--system-prompt`, `--append-system-prompt`, `--models`, `--thinking`, `--tools`, `--no-tools`, `--no-builtin-tools`, `--extension`, `--no-extensions`, `--skill`, `--no-skills`, `--prompt-template`, `--theme`, `--no-context-files`, `--offline`, `--verbose`, `--session-dir`.

Built-in tool names exposed by the CLI help: `read`, `bash`, `edit`, `write`, `grep`, `find`, `ls`.

Important environment variables:

| Area | Variables |
|---|---|
| Providers | `ANTHROPIC_API_KEY`, `ANTHROPIC_OAUTH_TOKEN`, `OPENAI_API_KEY`, `AZURE_OPENAI_API_KEY`, `AZURE_OPENAI_BASE_URL`, `AZURE_OPENAI_RESOURCE_NAME`, `AZURE_OPENAI_API_VERSION`, `AZURE_OPENAI_DEPLOYMENT_NAME_MAP`, `DEEPSEEK_API_KEY`, `GEMINI_API_KEY`, `GROQ_API_KEY`, `CEREBRAS_API_KEY`, `XAI_API_KEY`, `FIREWORKS_API_KEY`, `OPENROUTER_API_KEY`, `AI_GATEWAY_API_KEY`, `ZAI_API_KEY`, `MISTRAL_API_KEY`, `MINIMAX_API_KEY`, `OPENCODE_API_KEY`, `KIMI_API_KEY`, `CLOUDFLARE_API_KEY`, `CLOUDFLARE_ACCOUNT_ID`, `AWS_PROFILE`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_BEARER_TOKEN_BEDROCK`, `AWS_REGION` |
| Coding agent runtime | `PI_CODING_AGENT_DIR`, `PI_PACKAGE_DIR`, `PI_OFFLINE`, `PI_TELEMETRY`, `PI_SHARE_VIEWER_URL`, `PI_AI_ANTIGRAVITY_VERSION` |
| Slack bot | `MOM_SLACK_APP_TOKEN`, `MOM_SLACK_BOT_TOKEN` |
| Pods | `HF_TOKEN`, `PI_API_KEY`, `PI_CONFIG_DIR` |

### `mom` CLI surface

| Form | Behavior |
|---|---|
| `mom [--sandbox=host|docker:<name>] <working-directory>` | Starts Slack bot state under the working directory |
| `mom --download <channel-id>` | Downloads Slack channel data using bot token |

`mom` requires Slack app and bot tokens for normal bot mode. It uses per-channel state, event watching, Slack response/thread/file APIs, and agent runners that can execute on host or in a Docker sandbox.

### `pi-pods` CLI surface

| Form | Behavior |
|---|---|
| `pi pods setup <name> "<ssh>" --mount "<mount>"` | Configure a pod and install vLLM |
| `pi pods` | List pods |
| `pi pods active <name>` | Select active pod |
| `pi pods remove <name>` | Remove local pod config |
| `pi shell [name]` | Open interactive SSH shell |
| `pi ssh [name] "<command>"` | Run SSH command |
| `pi start <model> --name <name> [options]` | Start vLLM model |
| `pi stop [name]` | Stop one or all models |
| `pi list` | List running models |
| `pi logs <name>` | Stream model logs |
| `pi agent <name> [message...] [options]` | Chat with pod-hosted model through agent/tools |

Pod model options include `--memory`, `--context`, `--gpus`, `--vllm <args...>` and `--pod <name>`.

### `pi-ai` CLI surface

| Form | Behavior |
|---|---|
| `pi-ai login [provider]` | OAuth login, interactive provider selection when omitted |
| `pi-ai list` | List OAuth providers |

The helper currently writes `auth.json` in the current working directory, not the coding-agent auth path.

## Package inventory

### `packages/agent`

Purpose: stateful and stateless agent loop core.

Source areas:

| Area | Files |
|---|---|
| Loop | `src/agent-loop.ts` |
| Stateful wrapper | `src/agent.ts` |
| Types | `src/types.ts` |
| Proxy | `src/proxy.ts` |
| Tests | `test/agent-loop.test.ts`, `test/agent.test.ts`, `test/e2e.test.ts`, tool fixtures under `test/utils/` |

Runtime dependencies:

| Dependency | Role |
|---|---|
| `@mariozechner/pi-ai` | LLM stream, messages, tool validation |
| `typebox` | Tool schemas and type-safe validation data |

Build-time dependencies: `typescript`, `@types/node`.

Test-time dependencies: `vitest`, package fixtures in `test/utils`.

Current behavior notes:

- Agent loop streams assistant messages, executes tool calls, supports queued steering and follow-up messages.
- Tool execution mode defaults to parallel in the stateful `Agent` wrapper.
- Hooks exist before and after tool calls.

### `packages/ai`

Purpose: unified model registry, provider registry, streaming adapters, OAuth utilities and AI helper CLI.

Source areas:

| Area | Files |
|---|---|
| Public model/types API | `src/types.ts`, `src/models.ts`, `src/models.generated.ts`, `src/index.ts` |
| Provider registry | `src/api-registry.ts`, `src/providers/register-builtins.ts` |
| Providers | `src/providers/amazon-bedrock.ts`, `anthropic.ts`, `azure-openai-responses.ts`, `google.ts`, `google-gemini-cli.ts`, `google-vertex.ts`, `mistral.ts`, `openai-codex-responses.ts`, `openai-completions.ts`, `openai-responses.ts`, `faux.ts`, shared helpers |
| Streaming and validation | `src/stream.ts`, `src/utils/event-stream.ts`, `src/utils/json-parse.ts`, `src/utils/overflow.ts`, `src/utils/validation.ts` |
| OAuth | `src/oauth.ts`, `src/utils/oauth/*` |
| CLI | `src/cli.ts` |
| Generation scripts | `scripts/generate-models.ts`, `scripts/generate-test-image.ts` |

Built-in API adapters registered at module load: `anthropic-messages`, `openai-completions`, `mistral-conversations`, `openai-responses`, `azure-openai-responses`, `openai-codex-responses`, `google-generative-ai`, `google-gemini-cli`, `google-vertex`, `bedrock-converse-stream`.

Model registry providers present in `models.generated.ts`: `amazon-bedrock`, `anthropic`, `azure-openai-responses`, `cerebras`, `cloudflare-workers-ai`, `deepseek`, `fireworks`, `github-copilot`, `google`, `google-antigravity`, `google-gemini-cli`, `google-vertex`, `groq`, `huggingface`, `kimi-coding`, `minimax`, `minimax-cn`, `mistral`, `openai`, `openai-codex`, `opencode`, `opencode-go`, `openrouter`, `vercel-ai-gateway`, `xai`, `zai`.

Runtime dependencies:

| Dependency | Role |
|---|---|
| `@anthropic-ai/sdk`, `openai`, `@google/genai`, `@aws-sdk/client-bedrock-runtime`, `@mistralai/mistralai` | Provider SDKs |
| `undici`, `proxy-agent` | HTTP transport/proxy support |
| `partial-json` | Incremental/partial tool-call JSON handling |
| `typebox`, `zod-to-json-schema` | Tool schema representation/conversion |
| `chalk` | CLI formatting |

Build-time dependencies: `tsx` via root for generation, TypeScript compiler, generated model script inputs.

Test-time dependencies: `vitest`, `canvas`, image fixture `test/data/red-circle.png`, fake providers and fake HTTP behavior in tests.

Current behavior notes:

- Providers return `AssistantMessageEventStream`.
- Provider modules are lazily imported by `register-builtins.ts`.
- Tests cover aborts, streaming, tool call IDs, provider-specific payload quirks, OAuth, prompt cache, thinking/reasoning, unicode, overflow and fake provider flows.

### `packages/coding-agent`

Purpose: main CLI/TUI product and SDK around `agent` and `ai`.

Source areas:

| Area | Files |
|---|---|
| CLI bootstrap | `src/cli.ts`, `src/main.ts`, `src/cli/*`, `src/config.ts` |
| Modes | `src/modes/interactive/*`, `src/modes/rpc/*`, `src/modes/print-mode.ts` |
| Agent session | `src/core/agent-session*.ts`, `src/core/sdk.ts`, `src/core/messages.ts` |
| Tools | `src/core/tools/{read,bash,edit,write,grep,find,ls}.ts`, queue/truncate/render/path helpers |
| Execution | `src/core/bash-executor.ts`, `src/core/exec.ts` |
| Storage/config | `src/core/session-manager.ts`, `src/core/settings-manager.ts`, `src/core/auth-storage.ts`, `src/migrations.ts` |
| Extensions/resources | `src/core/extensions/*`, package manager, skills, prompts, themes |
| TUI components | `src/modes/interactive/components/*`, theme JSON and schema |
| Export | `src/core/export-html/*` with vendored `marked` and `highlight` browser assets |
| Bun binary support | `src/bun/*`, `build:binary` script |

Runtime dependencies:

| Dependency | Role |
|---|---|
| Workspace packages `pi-agent-core`, `pi-ai`, `pi-tui` | Agent core, provider streaming, terminal UI |
| `@mariozechner/jiti` | Load TS/JS extension resources |
| `@silvia-odwyer/photon-node` | Image processing / resize path |
| `chalk`, `cli-highlight`, `marked`, `strip-ansi` | Terminal and HTML rendering |
| `diff`, `glob`, `minimatch`, `ignore`, `hosted-git-info`, `proper-lockfile`, `uuid`, `yaml`, `file-type`, `extract-zip`, `undici`, `typebox` | Tools, sessions, settings, packages, network, content handling |
| Optional `@mariozechner/clipboard` | Native clipboard integration |

Build-time dependencies:

| Dependency/tool | Role |
|---|---|
| `tsgo` / TypeScript | Build `dist` |
| `shx` | Copy assets and chmod |
| `bun build --compile` | Optional compiled binary |
| Copied assets | Themes, PNGs, docs, examples, HTML export templates, `photon_rs_bg.wasm` |

Test-time dependencies: `vitest`, type packages, fixtures and extension examples.

Current behavior notes:

- Default config directory is `~/.pi/agent` unless `PI_CODING_AGENT_DIR` is set.
- User settings live at `settings.json`; project settings live at `.pi/settings.json`.
- Sessions are JSONL-like files under `sessions`, with session version migrations through v3 and tree/branch metadata.
- Bash execution streams output, sanitizes binary/ANSI output, writes large full output to a temp file, keeps truncated rolling output.
- Built-in TS extensions can register tools, commands, flags, UI widgets, custom renderers, keybindings, hooks, providers and auth flows.

### `packages/tui`

Purpose: custom terminal UI renderer, input/editor toolkit, key handling and components.

Source areas:

| Area | Files |
|---|---|
| Terminal/TUI core | `src/tui.ts`, `src/terminal.ts`, `src/stdin-buffer.ts`, `src/terminal-image.ts` |
| Input/editor | `src/editor-component.ts`, `src/components/editor.ts`, `src/components/input.ts`, `src/keybindings.ts`, `src/keys.ts`, `src/kill-ring.ts`, `src/undo-stack.ts` |
| Components | `src/components/{box,text,markdown,select-list,settings-list,image,loader,cancellable-loader,spacer,truncated-text}.ts` |
| Utilities | `src/fuzzy.ts`, `src/autocomplete.ts`, `src/utils.ts` |
| Tests | Node test runner tests under `test/*.test.ts`, virtual terminal helpers |

Runtime dependencies:

| Dependency | Role |
|---|---|
| `chalk` | ANSI styles |
| `get-east-asian-width` | Cell width |
| `marked` | Markdown parse/render |
| `mime-types` | Terminal image MIME handling |
| Optional `koffi` | Native integration path |

Build-time dependencies: TypeScript compiler.

Test-time dependencies: Node test runner, `tsx`, `@xterm/headless`, `@xterm/xterm`.

Current behavior notes:

- Tests cover keybindings, editor, markdown, wrapping, truncation, overlays, image output and regression cases.
- This package is the likely source for Go TUI golden tests and renderer behavior capture.

### `packages/mom`

Purpose: Slack bot product that delegates work to the coding agent.

Source areas:

| Area | Files |
|---|---|
| CLI/main loop | `src/main.ts` |
| Slack integration | `src/slack.ts`, `src/events.ts`, `src/download.ts` |
| Agent runner | `src/agent.ts`, `src/context.ts`, `src/sandbox.ts` |
| Persistence/logging | `src/store.ts`, `src/log.ts`, `src/fs-watch.ts` |
| Tools | `src/tools/{read,bash,edit,write,attach,truncate,index}.ts` |

Runtime dependencies:

| Dependency | Role |
|---|---|
| Workspace packages `pi-agent-core`, `pi-ai`, `pi-coding-agent` | Agent and coding tools |
| `@slack/socket-mode`, `@slack/web-api` | Slack transport and Web API |
| `@anthropic-ai/sandbox-runtime` | Sandbox support |
| `chalk`, `croner`, `diff`, `typebox` | Logging, scheduling/events, edit rendering, schemas |

Build-time dependencies: TypeScript compiler.

Test-time dependencies: none declared in package manifest.

Current behavior notes:

- Slack messages are truncated defensively around Slack limits.
- Working directory is split by Slack channel.
- Supports host and Docker sandbox modes.

### `packages/pods`

Purpose: GPU pod and vLLM deployment manager.

Source areas:

| Area | Files |
|---|---|
| CLI | `src/cli.ts` |
| Config/types | `src/config.ts`, `src/types.ts`, `src/model-configs.ts`, `src/models.json` |
| SSH | `src/ssh.ts` |
| Commands | `src/commands/{models,pods,prompt}.ts` |
| Package entry | `src/index.ts` |

Runtime dependencies:

| Dependency | Role |
|---|---|
| `@mariozechner/pi-agent-core` | Agent types/loop for pod chat path |
| `chalk` | CLI formatting |
| External system commands | `ssh`, pod shell, remote vLLM/Python install scripts |

Build-time dependencies: TypeScript compiler, copied `models.json` and `scripts/`.

Test-time dependencies: none declared in package manifest.

Current behavior notes:

- Local config defaults to `~/.pi` unless `PI_CONFIG_DIR` is set.
- Remote command execution is SSH based and streams output.
- The product intentionally orchestrates Python/vLLM remotely; this is not a local runtime requirement for the main Go `y`.

### `packages/web-ui`

Purpose: browser components for chat UI, provider/model settings and artifact rendering.

Source areas:

| Area | Files |
|---|---|
| Package entry/style | `src/index.ts`, `src/app.css` |
| Main UI | `src/ChatPanel.ts`, `src/components/*` |
| Dialogs | `src/dialogs/*` |
| Storage | `src/storage/*` |
| Tool renderers/artifacts | `src/tools/*`, `src/tools/artifacts/*` |
| Runtime bridge | `src/components/sandbox/*` |
| Prompts/utils | `src/prompts/prompts.ts`, `src/utils/*` |
| Example | `example/src/*`, Vite config through `example/package.json` |

Runtime dependencies:

| Dependency | Role |
|---|---|
| Workspace packages `pi-ai`, `pi-tui` | Shared AI/TUI types/helpers |
| `@lmstudio/sdk`, `ollama` | Local model/provider integrations |
| `docx-preview`, `jszip`, `pdfjs-dist`, `xlsx` | Document/artifact preview |
| `lucide` | Browser icons |
| `typebox` | Schemas |
| Peer `@mariozechner/mini-lit`, `lit` | Web component runtime |

Build-time dependencies:

| Dependency/tool | Role |
|---|---|
| TypeScript | Compile library |
| Tailwind CLI | Build `dist/app.css` |
| `concurrently` | Development watch |
| Vite in example | Example dev/build server |

Test-time dependencies: package check runs Biome and TypeScript for package and example. No unit test script is declared.

Current behavior notes:

- This package is browser JS by design. For Go migration, it should be treated as either server-rendered replacement or prebuilt static asset served by `y-web`.

## Dependency classification

### Runtime dependencies in the current product

These are required by at least one shipped Node/Bun runtime path today:

| Category | Dependencies / systems |
|---|---|
| Node/Bun runtime | Node >= 20 for npm package execution; Bun for compiled binary path |
| Workspace runtime packages | `pi-ai`, `pi-agent-core`, `pi-coding-agent`, `pi-tui`, `pi-web-ui` |
| Provider SDKs | Anthropic, OpenAI, Google GenAI, AWS Bedrock, Mistral |
| HTTP/proxy | `undici`, `proxy-agent` |
| Terminal/UI | `chalk`, `strip-ansi`, `marked`, `get-east-asian-width`, `mime-types`, optional `koffi` |
| Coding tools/session | `diff`, `glob`, `minimatch`, `ignore`, `proper-lockfile`, `uuid`, `yaml`, `file-type`, `extract-zip`, `cli-highlight`, `hosted-git-info` |
| Image/clipboard | `@silvia-odwyer/photon-node`, optional `@mariozechner/clipboard` |
| Slack/mom | Slack SDKs, sandbox runtime, `croner` |
| Pods | local `ssh` command and remote vLLM/Python environment |
| Browser UI | `lit`/mini-lit peer runtime, document preview libraries, local provider SDKs |

### Build-time dependencies

| Category | Dependencies / systems |
|---|---|
| TypeScript build | `typescript`, `@typescript/native-preview` / `tsgo`, package `tsconfig.build.json` files |
| Script runner | `tsx`, Node scripts |
| Monorepo helpers | `concurrently`, `shx`, `husky` |
| Formatting/check | `@biomejs/biome` |
| Browser CSS/build | Tailwind CLI, Vite for example |
| Binary packaging | `bun build --compile` and asset copy scripts |
| Model generation | `packages/ai/scripts/generate-models.ts` |

### Test-time dependencies

| Category | Dependencies / systems |
|---|---|
| Unit tests | `vitest` in `agent`, `ai`, `coding-agent` |
| TUI tests | Node `--test`, `tsx`, `@xterm/headless`, `@xterm/xterm` |
| Browser smoke/check | `scripts/check-browser-smoke.mjs`, web-ui Biome/TypeScript checks |
| AI fixtures | `canvas`, fake providers, fake HTTP servers, image fixture |
| Profiles/measurements | `scripts/profile-coding-agent-node.mjs` |

## Main functional flows

| Flow | Current packages | Current behavior to preserve or explicitly replace |
|---|---|---|
| Interactive coding session | `coding-agent`, `tui`, `agent`, `ai` | TUI starts, resolves config/model/provider/tools/resources, streams assistant output, executes tool calls, stores session |
| Headless prompt | `coding-agent`, `agent`, `ai` | `--print` reads args/stdin/files, runs agent, prints text or JSON |
| RPC | `coding-agent`, `agent`, `ai` | JSONL RPC mode with streamed events |
| Sessions | `coding-agent` | JSONL/session entries, v1-v3 migrations, resume/continue/fork/tree/labels/branch summaries |
| Tools | `coding-agent`, `agent` | Read, bash, edit, write, grep, find, ls with schemas, truncation, mutation queue and streaming bash |
| Provider streaming | `ai` | Normalized event stream over provider-specific SSE/SDK streams |
| Model selection | `ai`, `coding-agent` | Generated model registry plus settings/CLI model resolution and cycling |
| OAuth/auth | `ai`, `coding-agent` | Provider OAuth helpers and auth storage |
| Extensions | `coding-agent` | TS/JS extension loader with hooks, tools, commands, providers, UI primitives, custom messages |
| Package resources | `coding-agent` | Install/update/list resource packages from npm/git/local paths |
| Config/settings | `coding-agent` | Global and project JSON settings with lockfile; CLI flags override runtime choices |
| TUI rendering/input | `tui`, `coding-agent` | Renderer, components, editor, keybindings, overlays, terminal images |
| Slack automation | `mom`, `coding-agent`, `agent`, `ai` | Slack bot delegates agent work per channel and sandbox |
| Pod management | `pods` | Local config plus SSH orchestration of remote vLLM model servers |
| Browser UI | `web-ui` | Web component chat UI and artifact preview |

## Migration implications for Go

| Current area | Go target from spec | Notes |
|---|---|---|
| `packages/ai` | `pkg/ai`, `pkg/providers` | Preserve streaming and provider quirks; replace SDK use with direct HTTP where possible to keep binary small |
| `packages/agent` | `pkg/agent` | Preserve state machine, event stream, queued messages and tool loop semantics |
| `packages/coding-agent` | `pkg/coding`, `cmd/y` | Split CLI, session, config, tools, extensions and modes into Go packages |
| `packages/tui` | `pkg/tui` | Preserve input/editor/render behavior with golden tests |
| `packages/mom` | `pkg/mom`, `cmd/y-mom` | Keep as optional product/build tag |
| `packages/pods` | `pkg/pods`, `cmd/y-pods` | Keep remote Python/vLLM as external pod dependency, not local main runtime dependency |
| `packages/web-ui` | `pkg/web`, `cmd/y-web` | Prefer static artifacts or server-rendered replacement; browser JS cannot be replaced by Go runtime alone |
| TS/JS extensions | `pkg/extensions/wasm` | Existing TS extension API is not a target ABI; inventory only informs WASM V1 capabilities |

## Gaps for later inspection

Detailed gaps are tracked in `docs/baseline/gaps.md`. Highest-priority follow-up areas:

- Exact TUI rendering/keybinding parity matrix.
- Exact behavior matrix for every provider adapter and model compatibility branch.
- Session file compatibility requirements and migration policy.
- Extension API inventory vs WASM V1 scope.
- Package manager install/update behavior and security policy.
- Browser UI cutover decision.
