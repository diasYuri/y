# Migrating from `pi-mono` to `y`

This guide walks operators through cutting over from the legacy
TypeScript/Node `pi` runtime in `pi-mono` to the Go runtime in `y`.

The goals of the cutover are:

- Run a single static Go binary; remove Node, Bun, TypeScript, Python, and
  cgo from the runtime path.
- Keep the on-disk locations under `~/.pi/agent` so existing transcripts
  and config keep working.
- Preserve the providers, tools, and core flows that have a Go equivalent;
  document gaps explicitly for everything else.

## Audience and scope

This document is for anyone who currently runs `pi`, `pi-ai`, `mom`,
`pi-pods`, or `pi-web` and wants to switch to the Go binaries `y`,
`y-mom` and `y-pods`.

It assumes:

- You have a working `pi-mono` install today.
- You can build Go 1.25+ binaries (or download release artefacts produced
  from `y`).
- You can edit shell profiles and config files on the operator machine.

For build/install details, see `docs/release.md`. For provider-specific
behaviour see `docs/providers.md`. For the WASM extension story see
`docs/wasm-extensions.md` and `extension-wasm.md`.

## Naming changes

| Legacy (`pi-mono`)            | Go (`y`)                   |
|-------------------------------|---------------------------------|
| `pi`                          | `y`                             |
| `pi-ai login`                 | `y auth login`                  |
| `mom`                         | `y-mom`                         |
| `pi-pods` / `pi pods`         | `y-pods`                        |
| `~/.pi/agent` (state root)    | `~/.pi/agent` *(unchanged)*     |
| `PI_CODING_AGENT_DIR`         | `Y_CODING_AGENT_DIR`            |
| `PI_OFFLINE`                  | `Y_OFFLINE`                     |
| `PI_TELEMETRY`                | `Y_TELEMETRY`                   |

The default agent directory was kept intentionally to make rollbacks easy.

## Step 1 – Inventory the current install

Before installing `y`, capture the current `pi` setup so you can compare
behaviour after cutover:

```bash
pi --version
pi --list-models > pre-cutover-models.txt
pi config              # or inspect ~/.pi/agent/settings.json directly
ls ~/.pi/agent
ls ~/.pi/agent/sessions
```

Optional but helpful: snapshot the legacy state directory so a rollback is
trivial if something is missing in `y`:

```bash
cp -a ~/.pi/agent ~/.pi/agent.pre-y-cutover
```

Document the providers and API keys you actually use. The Go runtime
ships native clients for Anthropic, Google, OpenAI, and OpenAI-compatible
endpoints; less common providers from `packages/ai` (Bedrock, Mistral,
Vertex, Azure-Responses, Codex Responses, etc.) are tracked in
`docs/baseline/gaps.md` and are not yet ported.

## Step 2 – Install the Go binaries

Follow `docs/release.md` to either build from source or install a release
artefact:

```bash
# From a release archive.
sudo install -m 0755 dist/y-linux-amd64 /usr/local/bin/y

# Or from source with the standard profile.
cd y
CGO_ENABLED=0 go build \
  -tags "feature_fs feature_git feature_shell feature_openai feature_anthropic feature_google feature_local" \
  -o /usr/local/bin/y ./cmd/y
```

Check the install:

```bash
y --version
y features
y doctor
```

You can keep `pi` installed in parallel during cutover; the binaries do
not collide.

## Step 3 – Map config from JSON to TOML

`pi-mono` stored settings as JSON (`~/.pi/agent/settings.json`). The Go
runtime uses TOML at `~/.pi/agent/config.toml` and a strict schema enforced
by `internal/config`. Migrate by hand: there is no automatic converter,
because the Go schema is intentionally narrower (no extension-defined
keys, no resource manager state).

Common translations:

| Legacy key (JSON)                  | New key (TOML)                                    | Notes                                                           |
|------------------------------------|---------------------------------------------------|-----------------------------------------------------------------|
| `tools` allow lists                | `[tools]` per-tool booleans                       | Keep only tools whose feature tag was compiled into `y`.        |
| `provider` / `model`               | CLI flags `--provider`, `--model`                 | The Go runtime does not read provider defaults from config yet. |
| `sessionDir`                       | `--session-dir <dir>` flag (per command)          | Defaults to `~/.pi/agent/sessions`. Override via env if needed. |
| `theme`                          | Not yet ported.                                    | Theme presets from `pi` are not yet ported.                    |
| `extensions` (TS)                  | WASM extensions (`docs/wasm-extensions.md`)       | TS extension API is **not** a target ABI.                       |

Validate the new config before running the binary:

```bash
y config validate --config ~/.pi/agent/config.toml
```

If you see `feature "<x>" requested by config but not compiled into this
binary`, either rebuild with the matching tag or disable the feature in
config.

## Step 4 – Migrate authentication

`y` supports both environment variables and an OAuth login flow.

### Environment variables (fastest)

Configure shell profiles (`.zshrc`, `.bashrc`, `direnv`, or platform secret
manager) with the env vars listed in `docs/release.md`:

```bash
export OPENAI_API_KEY="sk-..."
export ANTHROPIC_API_KEY="sk-ant-..."          # or ANTHROPIC_OAUTH_TOKEN
export GEMINI_API_KEY="AIza..."
export OPENAI_COMPATIBLE_API_KEY="..."         # if using local providers
export Y_OPENAI_COMPATIBLE_ALLOW_EMPTY_KEY=true # only for unauth local stacks
```

### OAuth login (`y auth login`)

The Go runtime now implements the same OAuth flows as `pi-ai login`:

```bash
y auth login --provider anthropic   # PKCE + loopback
y auth login --provider openai      # PKCE + loopback
y auth login --provider google      # PKCE + loopback
y auth login --provider github_copilot  # Device code flow
```

Credentials are stored in `~/.pi/agent/auth.json` (same path as the legacy
runtime) and are read automatically when the corresponding env var is not
set. List stored credentials with `y auth list` and remove them with
`y auth logout --provider <name>`.

Optional:

```bash
export Y_PROVIDER=anthropic        # equivalent to passing --provider on every call
export Y_CODING_AGENT_DIR=~/.pi/agent
```

## Step 5 – Migrate providers

Follow this provider checklist:

1. **Anthropic.** Set `ANTHROPIC_OAUTH_TOKEN` (preferred) or
   `ANTHROPIC_API_KEY`. Run `y run --provider anthropic "ping"`.
2. **OpenAI.** Set `OPENAI_API_KEY`. Run `y run --provider openai "ping"`.
3. **Google.** Set `GEMINI_API_KEY`. Run `y run --provider google "ping"`.
4. **Local / OpenAI-compatible.** Set `OPENAI_COMPATIBLE_API_KEY` (or
   enable `Y_OPENAI_COMPATIBLE_ALLOW_EMPTY_KEY=true`) and pass
   `--model <id>` whose `BaseURL` points at your endpoint, or use the
   provider package directly via the SDK.
5. **Anything else.** Confirm the provider is on the gap list in
   `docs/baseline/provider-matrix.md` and `docs/baseline/gaps.md`. Until
   the Go port lands, route those flows through the legacy `pi` binary.

The auto-detection order when neither `--provider` nor `Y_PROVIDER` is set
is `openai → anthropic → google → local`. Set `Y_PROVIDER` to make the
choice deterministic across machines.

## Step 6 – Migrate sessions

Sessions are persisted at:

```
~/.pi/agent/sessions/<workspace-encoded>/<session-id>.jsonl
```

The Go session format is JSONL. The first record is a `session` header
followed by `message`/`truncation` records (see `docs/sessions.md` for the
exact schema). Legacy `pi-mono` sessions are similar but include
`pi-coding-agent`-specific metadata (tree branches, compaction summaries,
v3 migration markers).

For most users:

- Reading old transcripts with `y session show <id>` works for plain
  message records.
- Records that depend on TS-only metadata (skill invocations, custom
  message types from extensions) may show up as raw JSON lines.
- Resume / fork / tree workflows from the legacy CLI are not yet ported;
  they are tracked in `docs/baseline/cli-matrix.md` and
  `docs/baseline/gaps.md`.

If you need the legacy resume flow, keep using `pi` until the Go session
manager catches up.

## Step 7 – Migrate extensions (WASM)

The legacy `packages/coding-agent/examples/extensions` tree is
TypeScript/JavaScript and is **not portable** to the Go runtime. The Go
extension story is WASM-only and intentionally narrower (V1 covers tools
and commands; providers are deferred per `extension-wasm.md` §19).

Migration path:

1. Build `y` with `feature_wasm_ext`.
2. Place a WASM extension under `~/.pi/agent/extensions/<id>/` with an
   `extension.toml` and `module.wasm`.
3. Run `y extension validate ./<id>` to check the manifest.
4. Run `y extension list` to confirm discovery.
5. Toggle with `y extension enable <id>` / `y extension disable <id>`.

A working TinyGo example lives in `examples/extensions/hello`; reuse it
as the starter template. TS extensions stay supported only via the legacy
`pi` binary.

## Step 8 – Migrate `mom` and `pi-pods`

### `mom` → `y-mom`

```bash
go build -tags "feature_mom feature_anthropic" -o bin/y-mom ./cmd/y-mom
y-mom --help
```

Set `MOM_SLACK_APP_TOKEN`, `MOM_SLACK_BOT_TOKEN`, and the provider
credentials you need. The working-directory layout (`MEMORY.md`,
`events/`, per-channel folders, `log.jsonl`) is unchanged from
`pi-mono`'s `mom`. See `docs/y-mom.md` for the full layout, sandbox
options, and event schemas.

The current build ships `FakeConnector` so tests do not require Slack;
real Socket Mode support is gated behind a future `feature_mom_slack_live`
build tag (see *Future work* in `docs/y-mom.md`).

### `pi-pods` → `y-pods`

```bash
go build -tags "feature_pods" -o bin/y-pods ./cmd/y-pods
y-pods pods list
```

`y-pods` keeps the pod registry under `~/.pi` (or `Y_PODS_CONFIG_DIR`).
Setup, list, active, remove, ssh, start, and stop commands work today;
`shell`, `logs`, and `agent` are tracked as gaps in
`docs/baseline/gaps.md`. Use `pi-pods` for those flows until the Go port
catches up.

## Step 8 – Verify the cutover

Run the smoke tests from `docs/release.md` against the new binary:

```bash
y --version
y doctor --json | jq '.status'
y features
y config validate --config ~/.pi/agent/config.toml
y run --provider anthropic "say hi in one word"
y session list
```

Compare the model list and provider behaviour against the snapshot you
took in Step 1. Anything that is not yet supported by `y` should map to a
documented entry in `docs/baseline/gaps.md`.

## Step 9 – Roll back

If you need to fall back to `pi`:

1. The legacy binary is still on `PATH` (you only added `y` in Step 2).
2. Restore the snapshot from Step 1 if you edited `~/.pi/agent`:
   ```bash
   mv ~/.pi/agent ~/.pi/agent.y-cutover
   mv ~/.pi/agent.pre-y-cutover ~/.pi/agent
   ```
3. Resume your previous workflows with `pi`. The Go runtime never deletes
   legacy session files; it only adds new ones in the same directory.

Roll forward again by reverting step 2 and re-running `y doctor`.

## Known gaps and workarounds

The migration is incremental. Anything still missing on the Go side is
documented in `docs/baseline/gaps.md`. Highlights as of phase 9:

- OAuth login flows (`pi-ai login`, `/login`, `/logout`) are not yet
  implemented; use env vars for auth.
- Resume/fork/tree session workflows are not yet ported.
- TS extensions are intentionally not portable; rebuild as WASM.
- Less common providers (Bedrock, Mistral, Vertex, Azure-Responses, Codex
  Responses, …) live only in `packages/ai` and require the legacy
  binary.
- Telemetry, RPC mode, and LSP integration are gated by feature tags that
  are not yet implemented in this build.

When in doubt, run both binaries side-by-side until your workflow is
covered end-to-end on `y`.
