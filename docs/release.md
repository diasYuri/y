# Release & Install Guide

This document describes how to build, install, and operate the Go binaries
that ship from `y`. The runtime targets a single static Go binary per
platform; no Node, Bun, TypeScript, Python, or `cgo` is required at runtime.

The matrix below covers the three reference build profiles, the build
metadata that is injected via `-ldflags`, the per-binary configuration and
authentication surface, and the optional WASM extension host.

## Quick start

```bash
# Clone or copy y.
cd y

# Run the test suite.
go test ./...

# Build a minimal `y` binary into ./bin.
mkdir -p bin
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/y ./cmd/y
```

Verify the binary:

```bash
./bin/y --version
./bin/y features
./bin/y doctor
```

`y doctor` prints version, commit, build date, build tags, runtime info, and
the count of compiled-vs-known capabilities. `y features` lists every
capability the binary recognises and whether it was compiled in.

## Build profiles

Builds are selected with Go build tags. Tags only enable code that has been
compiled in; runtime configuration cannot enable a feature whose tag is
missing. The reference profiles correspond to the targets listed in the
migration spec (`y-migration.md` §4 and §10).

| Profile      | Goal                                                                     | Build tags                                                                                                                                | Approx target RSS / size |
|--------------|--------------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------|--------------------------|
| `y-minimal`  | Headless agent for restricted environments (CLI + HTTP provider + FS).   | `feature_fs feature_openai`                                                                                                               | 8–25 MB RSS / 8–20 MB    |
| `y-standard` | Default developer experience with git, shell, and primary providers.     | `feature_fs feature_git feature_shell feature_openai feature_anthropic feature_google feature_local`                                      | 20–60 MB RSS / 20–60 MB  |
| `y-full`     | Everything compiled, including the optional WASM extension host.         | `feature_fs feature_git feature_shell feature_openai feature_anthropic feature_google feature_local feature_wasm_ext`                     | 40–100 MB RSS / 40–100 MB|

The full matrix of known build tags lives in
`internal/feature/catalog.go`. The most common combinations are:

```bash
# Minimal: HTTP provider + filesystem tools, no shell.
go build -tags "feature_fs feature_openai" -o bin/y-minimal ./cmd/y

# Standard: git + shell + main providers.
go build \
  -tags "feature_fs feature_git feature_shell feature_openai feature_anthropic feature_google feature_local" \
  -o bin/y-standard ./cmd/y

# Full: standard tags + WASM extension host.
go build \
  -tags "feature_fs feature_git feature_shell feature_openai feature_anthropic feature_google feature_local feature_wasm_ext" \
  -o bin/y-full ./cmd/y
```

Always pass `CGO_ENABLED=0`, `-trimpath`, and `-ldflags="-s -w"` for release
binaries. A complete release command looks like:

```bash
CGO_ENABLED=0 go build \
  -trimpath \
  -tags "feature_fs feature_git feature_shell feature_openai feature_anthropic feature_google feature_local" \
  -ldflags "\
    -s -w \
    -X github.com/yuri/y/internal/buildinfo.version=$VERSION \
    -X github.com/yuri/y/internal/buildinfo.commit=$(git rev-parse HEAD) \
    -X github.com/yuri/y/internal/buildinfo.date=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
    -X github.com/yuri/y/internal/buildinfo.tags=feature_fs,feature_git,feature_shell,feature_openai,feature_anthropic,feature_google,feature_local" \
  -o bin/y ./cmd/y
```

`internal/buildinfo` exposes those fields through `y --version` and the
`doctor` report. Releases that do not inject metadata still build, but
`y --version` reports `0.0.0-dev`.

## Cross-compilation matrix

```bash
GOOS=darwin  GOARCH=arm64  CGO_ENABLED=0 go build -o dist/y-darwin-arm64       ./cmd/y
GOOS=darwin  GOARCH=amd64  CGO_ENABLED=0 go build -o dist/y-darwin-amd64       ./cmd/y
GOOS=linux   GOARCH=amd64  CGO_ENABLED=0 go build -o dist/y-linux-amd64        ./cmd/y
GOOS=linux   GOARCH=arm64  CGO_ENABLED=0 go build -o dist/y-linux-arm64        ./cmd/y
GOOS=windows GOARCH=amd64  CGO_ENABLED=0 go build -o dist/y-windows-amd64.exe  ./cmd/y
```

Add the desired build-tag set with `-tags "..."` for each target. Because
the runtime is `CGO_ENABLED=0`, no toolchain other than the Go compiler is
required for cross builds.

## Binaries shipped in y

The repository produces three binaries from `cmd/`:

| Binary    | Source                | Purpose                                                                |
|-----------|-----------------------|------------------------------------------------------------------------|
| `y`       | `cmd/y/main.go`       | Primary CLI; coding agent and headless run/chat.                       |
| `y-mom`   | `cmd/y-mom/main.go`   | Slack automation product, gated by `feature_mom`.                      |
| `y-pods`  | `cmd/y-pods/main.go`  | GPU pod / vLLM management product, gated by `feature_pods`.            |

```bash
# Build every secondary binary alongside the primary.
CGO_ENABLED=0 go build -tags "feature_mom feature_anthropic" -o bin/y-mom  ./cmd/y-mom
CGO_ENABLED=0 go build -tags "feature_pods"                  -o bin/y-pods ./cmd/y-pods
```

Binaries built without their corresponding feature tag still link, but their
top-level commands stub out. Use `--features` and `doctor` to confirm what
was compiled in.

## Installation

The recommended install paths:

```bash
# 1. Drop the static binary somewhere on PATH.
sudo install -m 0755 dist/y-linux-amd64 /usr/local/bin/y

# 2. (Optional) install the secondary binaries with the same pattern.
sudo install -m 0755 dist/y-mom-linux-amd64  /usr/local/bin/y-mom
sudo install -m 0755 dist/y-pods-linux-amd64 /usr/local/bin/y-pods

# 3. Confirm the install.
y --version
y features
y doctor --json | jq '.status'
```

User state lives under `~/.pi/agent` by default. Override with
`Y_CODING_AGENT_DIR=<path>`. The directory holds `config.toml`, `auth.json`,
and the per-workspace `sessions/` folder. See `docs/sessions.md` for the
on-disk layout.

## Configuration

`y` reads a TOML config file. By default it is
`~/.pi/agent/config.toml`; override with `--config <path>` on
`y config validate` (and equivalent flags as they are added).

The accepted sections and keys are enforced by `internal/config`:

```toml
[features]
filesystem       = true   # requires feature_fs
shell            = false  # requires feature_shell
git              = true   # requires feature_git
lsp              = false  # requires feature_lsp
rpc              = false  # requires feature_rpc
mom              = false  # requires feature_mom
pods             = false  # requires feature_pods
telemetry        = false  # requires feature_telemetry
wasm_extensions  = false  # requires feature_wasm_ext

[providers]
openai     = true   # requires feature_openai
anthropic  = false  # requires feature_anthropic
google     = false  # requires feature_google
local      = false  # requires feature_local

[tools]
read_file    = true
write_file   = true
list_files   = true
search       = true
edit         = true
patch        = true
run_command  = false
git_status   = true
git_diff     = true
git_commit   = false

[limits]
max_file_read_bytes      = 1048576
max_command_output_bytes = 262144
max_session_bytes        = 8388608
max_parallel_tools       = 4
command_timeout_seconds  = 30
```

Validate before deployment:

```bash
y config validate --config /path/to/config.toml
```

If the config enables a capability that was not compiled into the binary,
validation exits non-zero with the message:

```
feature "git" requested by config but not compiled into this binary
```

This is the contract from `y-migration.md` §11. Runtime config never
expands the binary's capabilities; it only narrows or pre-selects what is
already available.

## Authentication

`y` keeps secrets in environment variables; an `auth.json` store under the
agent directory is reserved for stored OAuth/state but is not yet authored
by this build (the legacy `pi-ai` helper wrote `auth.json` in the cwd; the
Go runtime does not).

| Provider          | Default API endpoint                                                                                                | Required env var(s)                                                            | Override flag |
|-------------------|---------------------------------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------|---------------|
| OpenAI            | `https://api.openai.com/v1/chat/completions`                                                                        | `OPENAI_API_KEY`                                                               | `--api-key`   |
| Anthropic         | `https://api.anthropic.com/v1/messages`                                                                             | `ANTHROPIC_OAUTH_TOKEN` *(preferred)* or `ANTHROPIC_API_KEY`                   | `--api-key`   |
| Google Gemini     | `https://generativelanguage.googleapis.com/v1beta/models/{model}:streamGenerateContent?alt=sse`                     | `GEMINI_API_KEY`                                                               | `--api-key`   |
| OpenAI-compatible | `http://localhost:11434/v1/chat/completions` (configurable via `--model` `BaseURL` / `WithBaseURL`)                 | `OPENAI_COMPATIBLE_API_KEY` or `Y_OPENAI_COMPATIBLE_API_KEY`                   | `--api-key`   |

For local OpenAI-compatible servers that do not require auth, set
`Y_OPENAI_COMPATIBLE_ALLOW_EMPTY_KEY=true`.

`Y_PROVIDER=<name>` selects the active provider when neither `--provider`
nor a single discoverable env-var key is present. The auto-detection order
is `openai → anthropic → google → local`.

`y run` and `y chat` share the same provider factory in
`internal/app/provider_factory.go`.

## Providers

`pkg/providers` ships native Go clients that stream over HTTP/SSE and emit
the unified events from `pkg/ai`:

- `pkg/providers/anthropic`
- `pkg/providers/google`
- `pkg/providers/openai`
- `pkg/providers/openai_compatible`

`docs/providers.md` documents each provider's auth precedence, request
headers, and configuration knobs.

Stream events emitted to the agent loop:

| Provider event       | `pkg/ai` event   |
|----------------------|------------------|
| Text token           | `ai.TextDelta`   |
| Tool call            | `ai.ToolCallEvent` |
| Token accounting     | `ai.UsageEvent`  |
| Terminal/end-of-turn | `ai.StopEvent`   |
| Stream error         | `ai.ErrorEvent`  |

Provider-specific OAuth flows, model registry overrides, retry tuning, and
compatibility flags beyond the matrix above are tracked in
`docs/baseline/gaps.md`.

## Extensions (WASM)

The optional WASM extension host follows the contract in
`extension-wasm.md` (ABI `pi.wasm.v1`) and `docs/wasm-extensions.md`.

Build with the host enabled:

```bash
go build -tags "feature_wasm_ext" ./cmd/y
```

A binary built **without** `feature_wasm_ext`:

- Excludes the host code paths entirely.
- Refuses any config that sets `wasm_extensions = true` with the message
  `feature "wasm_extensions" requested by config but not compiled into this binary`.
- Returns `wasm.ErrHostUnavailable` from `Manager` calls and prints
  `y: extension commands are unavailable in this build (missing feature_wasm_ext)`.

A binary built **with** `feature_wasm_ext`:

- Discovers manifests under `~/.pi/agent/extensions` and `./.y/extensions`
  by default; override with `--dir <path>` per command.
- Toggles individual extensions via `~/.pi/agent/extensions.toml`.
- Loads modules lazily on first use.
- Enforces the deny-by-default capability matrix from
  `docs/wasm-extensions.md`. Capabilities are intersected from the manifest,
  the host config allow list, and the runtime policy gate.
- Enforces per-call limits (`TimeoutMS=5000`, `MemoryPages=256`,
  `MaxInputBytes/OutputBytes=1 MiB`, `MaxLogBytes=64 KiB`,
  `MaxHostCalls=128` by default). Timeouts and traps surface as
  structured `*wasm.ExtensionError` and never tear down the host.

CLI surface (only registered when the tag is present):

```text
y extension list   [--dir <path>]
y extension info   [--dir <path>] <id>
y extension validate <path>
y extension enable  <id>
y extension disable <id>
```

A working TinyGo example lives in `examples/extensions/hello`. It ships a
manifest, a `tinygo/build.sh`, and a regression test that does not require
TinyGo to be installed on the host.

## Diagnostics

| Command                       | Purpose                                                          |
|-------------------------------|------------------------------------------------------------------|
| `y --version`                 | Print the build version string from `internal/buildinfo`.        |
| `y features`                  | Tabular view of every known capability and whether it compiled. |
| `y config validate [path]`    | Parse + validate a TOML config against the compiled capabilities.|
| `y doctor [--json]`           | Build metadata, runtime info, capability counts, env checks.     |
| `y session list`              | List saved transcripts for the current workspace.                |
| `y session show <id>`         | Print a saved transcript as JSONL.                               |
| `y extension list / info`     | (Only with `feature_wasm_ext`.) Inspect WASM extensions.         |

`y doctor` exit codes match the headless command codes from
`docs/run-chat.md`:

| Code | Meaning                                            |
|------|----------------------------------------------------|
| 0    | Success.                                           |
| 2    | Usage error.                                       |
| 3    | Configuration error (missing/unavailable capability). |
| 4    | Execution error (provider, agent, storage).        |
| 130  | Interrupted or canceled.                           |

## Verification checklist

Before declaring a release candidate ready, the following must pass on at
least one supported platform per profile:

- `go vet ./...`
- `gofmt -l .` reports no diffs
- `go test ./...`
- `y-minimal --version && y-minimal doctor`
- `y-standard run "say hi"` against a real provider, with secrets pulled
  from environment variables only (no `--api-key` literals in scripts).
- `y-full extension list --dir examples/extensions` shows the example
  hello extension.
- `y-mom --help` prints usage; if Slack tokens are present in CI, run a
  short Socket Mode handshake test.
- `y-pods --help` and `y-pods pods list` against a local config dir.

## Build & release scripts

The repository ships portable bash wrappers under `scripts/` so contributors and
CI agree on the same commands.

```bash
scripts/build.sh   --binary <y|y-mom|y-pods|all> --flavor <minimal|standard|full>
scripts/release.sh --version <ver> --flavor <flavor> [--os <list> --arch <list>]
scripts/check.sh   <fmt|vet|test|test-all|build|all>
```

`build.sh` is the canonical builder. It cross-compiles to any GOOS/GOARCH pair
(or the full reference matrix via `--matrix`), enforces `CGO_ENABLED=0`,
applies `-trimpath`, injects build metadata via `-ldflags`, and emits files
named:

```
bin/<binary>-<flavor>-<os>-<arch>[.exe]
```

`release.sh` wraps `build.sh`, then assembles deterministic archives:

```
dist/y-<version>-<flavor>-<os>-<arch>.tar.gz   (unix)
dist/y-<version>-<flavor>-windows-<arch>.zip   (windows)
dist/SHA256SUMS
```

Each archive bundles the binaries that the flavor compiles, plus `LICENSE` and
the user-facing docs (`docs/release.md`, `docs/migration-from-pi.md`).

`check.sh` is the formatting/vet/test gate. The `fmt` step runs
`gofmt -l .` and **fails non-zero whenever any path appears in the output**
(see acceptance for `phase-9-ci-release-artifacts`). The `test-all` step runs
`go test` with every `feature_*` tag enabled, so tag-gated packages such as
`pkg/extensions/wasm` (manager-enabled paths) are exercised.

A convenience `Makefile` wraps the same commands (`make check`, `make build`,
`make release VERSION=v0.1.0 FLAVOR=full`).

## Continuous integration

`.github/workflows/ci.yml` runs on every push and pull request to `main`:

| Job             | What it does                                                                 |
|-----------------|------------------------------------------------------------------------------|
| `lint`          | `scripts/check.sh fmt` + `scripts/check.sh vet`. Fails on any unformatted Go.|
| `test`          | `scripts/check.sh test` + `scripts/check.sh test-all` on linux/macos/windows.|
| `build-by-tag`  | `scripts/build.sh --binary all --flavor {minimal,standard,full}` on linux.   |

`.github/workflows/release.yml` runs on `v*` tags or manual dispatch. It runs
the full `check.sh`, then `release.sh` with the resolved version, uploads the
artefacts (and `SHA256SUMS`) to the workflow run, and—when triggered by a
real tag push—publishes/refreshes the GitHub release.

## Reference build matrix

The default matrix in `build.sh --matrix` and `release.sh` mirrors §26 of
`y-migration.md`:

```
darwin/arm64   darwin/amd64
linux/amd64    linux/arm64
windows/amd64
```



The recommended release layout published from CI is:

```
y-<version>-<flavor>-darwin-arm64.tar.gz
y-<version>-<flavor>-darwin-amd64.tar.gz
y-<version>-<flavor>-linux-amd64.tar.gz
y-<version>-<flavor>-linux-arm64.tar.gz
y-<version>-<flavor>-windows-amd64.zip
SHA256SUMS
```

`<flavor>` is one of `minimal`, `standard`, `full`, mirroring the build profile
(`scripts/release.sh --flavor ...`). Each archive contains:

```
y                                  # primary binary
y-mom        (when feature_mom)    # secondary binaries when their tags compile
y-pods       (when feature_pods)
LICENSE
docs/release.md
docs/migration-from-pi.md
```

CI bootstraps the cross-compilation matrix with the standard build tag set
unless an artefact is explicitly tagged `minimal` or `full`. Each archive
embeds the same `version`, `commit`, `date`, and `tags` metadata via
`-ldflags`, so `y doctor --json` is the source of truth for "what is
inside this binary".
