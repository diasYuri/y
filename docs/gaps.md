# Migration gaps

This file is the consolidated, living list of gaps identified during the
`pi-mono` → `y` migration. It supersedes the per-phase notes that
remain in `docs/baseline/gaps.md` (kept for historical reference); when
the two disagree, this file is authoritative.

Each gap describes:

- the missing or deferred behaviour,
- the spec section / phase it relates to (`y-migration.md` and
  `extension-wasm.md`),
- the rationale for deferring it, and
- the recommended follow-up. Some gaps are intentional design decisions
  (no JS/Python in WASM V1) rather than incomplete work.

Severity is interpreted as the impact on shipping `y` as the
default runtime today, not as the long-term importance of the feature.

## High severity

| Gap | Spec ref | Rationale | Follow-up |
|-----|----------|-----------|-----------|
| `mom` Slack workflow only covers the in-process pieces (events, store, server). The host/Docker sandbox from `pi-mom` (`packages/mom/src/sandbox.ts`) is implemented as a fake (`pkg/mom/fakes.go`); production Docker integration is gated until policy capabilities are agreed. | `y-migration.md` §15, §18; `pi-mono/packages/mom` | Docker sandboxing needs explicit capability + secret policy; shipping a half-done implementation would be worse than the current "feature flagged off" state. | Define `mom.docker_sandbox` capability in `internal/policy`, then implement the runner in `pkg/mom/sandbox.go` behind it. Reuse the spec's tool-limits framework. |

## Medium severity

None currently.

## Low severity / informational

| Gap | Spec ref | Rationale | Follow-up |
|-----|----------|-----------|-----------|
| Configuration ingest only supports TOML (per spec). Legacy JSON config from `pi-mono` is not auto-imported. | `y-migration.md` §11; `docs/migration-from-pi.md` | Auto-import would muddy validation errors. The migration guide instead documents the required keys and links to a worked example. | Optional: add a `y config import --from-pi` helper that converts `~/.pi/agent/settings.json` to TOML. |
| Agent steering / before-after-tool hooks (`agent-loop.ts` callbacks) do not have a Go equivalent. | `y-migration.md` §13 | The Go state machine covers the multi-turn loop; hook callbacks were primarily used by TS extensions, which are intentionally not supported in V1 of the WASM host. | Re-introduce as host-call hooks if WASM extensions need them; otherwise keep deferred. |
| Generated provider model lists are hand-curated for Go (no parity script with `pi-mono/packages/ai/scripts/generate-models.ts`). | `y-migration.md` §14 | Avoids running a TS script in the Go pipeline. The provider tests assert the curated lists. | RESOLVED: each provider subpackage has a `models.json` declarative source and a generated `models_gen.go` (`go generate ./pkg/providers/...` or `make models`). The generator lives in `scripts/models-gen/`; concrete providers expose `CuratedModels()` and use it as the offline fallback. |
| Extension SDKs only cover TinyGo. Rust SDK is documented in `extension-wasm.md` §24 but not yet provided. | `extension-wasm.md` §24 | TinyGo was the smallest viable SDK for V1; Rust adds toolchain expectations that vendors can already replicate using the documented ABI. | Publish a Rust SDK crate in a follow-up phase; keep the ABI spec authoritative. |
| Browser smoke test artefacts (`scripts/measure-baseline.mjs`) still call into Node for measurement, not for runtime. | `y-migration.md` §4 | Measurement harness is allowed to depend on Node/Bun (build-time tooling) since it is not part of the shipped binaries. | Optional: rewrite as a Go binary so that operators do not need Node to reproduce baseline measurements. |
| `pi-mono` package manager (npm/git/HTTPS install paths for skills) is not reproduced in `y`. | `y-migration.md` §15 (tools) | The runtime no longer wants to fetch arbitrary code at runtime. WASM extensions are the supported path. | Document this as a deliberate non-goal; if needed for compatibility, add a `y skills install` command behind `feature_skills` that vendors curated payloads. |
| Sessions on disk are JSONL only; SQLite mode mentioned in `y-migration.md` §17 is not implemented. | `y-migration.md` §17 | JSONL works for the current scale of transcripts and avoids a cgo-free SQLite dependency. | If transcript scale becomes a problem, add SQLite via `modernc.org/sqlite` (pure Go) behind `feature_storage_sqlite`. |
| `Y_OFFLINE` and `Y_TELEMETRY` env vars are documented but not yet wired through every code path. | `y-migration.md` §22; `docs/migration-from-pi.md` | Offline mode currently relies on per-provider error handling; telemetry is gated by build tag. | Centralise in `internal/config` so all providers honour the flag, and add a doctor check that surfaces the effective values. |
| SQLite session storage (`feature_storage_sqlite`) | `y-migration.md` §17 | Implemented behind build tag. Requires `go get modernc.org/sqlite` before building with the tag. | Run `go get modernc.org/sqlite` then build with `-tags feature_storage_sqlite`. |

## Intentional non-goals (kept for traceability)

| Decision | Spec ref |
|----------|----------|
| Do not run Node/Bun/Python in production. | `y-migration.md` §1, §3 |
| Do not use Go `plugin` for native extensions. | `y-migration.md` §5.3 |
| Do not enable cgo in the core. | `y-migration.md` §5.4 |
| Do not maintain the TS internal API surface. | `y-migration.md` §3 |
| Do not run JS or Python inside WASM in V1. | `extension-wasm.md` §3, §32 |
| Do not auto-grant capabilities; deny by default. | `extension-wasm.md` §15 |
| Do not allow WASM extensions to register providers in V1. | `extension-wasm.md` §19 |

## Resolved

| Gap | Resolution |
|-----|------------|
| Auth/login flow (`y auth login`, `y auth list`, `y auth logout`) | Implemented in `internal/auth/` with PKCE + loopback for Anthropic, OpenAI, Google; device code flow for GitHub Copilot. Auth store reads from `~/.pi/agent/auth.json`. Provider factory falls back to auth store when env var is not set. CLI wired in `internal/app/auth.go`. |
| Compaction / branch summarisation pipeline | Implemented in `pkg/agent/compaction/` with token estimation, threshold trigger, LLM summarization prompt, and transcript rewrite. Integrated into `pkg/agent/agent.go` after each turn. |
| Telemetry collection | Implemented in `internal/telemetry/` with event schema (`EventAgentTurn`, `EventToolCall`, `EventProviderRequest`), OTLP/HTTP exporter behind `feature_telemetry`, noop fallback. `Y_TELEMETRY` and `Y_TELEMETRY_ENDPOINT` env vars honoured. Doctor checks report status. Instrumented in headless agent loop. |
| WASM `process.exec` capability | Wired in `pkg/extensions/wasm/hostfuncs.go` with `dispatchProcessExec`. Enforces capability grant, policy gate, timeout, output limits. Returns structured `HostProcessExecResult`. Tests cover success, capability denial, invalid payload, timeout. |
| Search `.gitignore` semantics | Implemented `internal/gitignore/` with parser for glob, negation (`!`), anchoring (`/`), directory-only (`/`), and `**` wildcard. `WalkIgnore` collects per-directory `.gitignore` files during `filepath.WalkDir` and applies them hierarchically. Integrated into `pkg/tools/filesystem.go` search tool. |
| Pods setup scripts via `//go:embed` | `pkg/pods/scripts/model_run.sh` embedded via `//go:embed`. `buildModelRunScript` reads the template and substitutes variables. |
| `Y_OFFLINE` / `Y_TELEMETRY` env vars | Read in `internal/config/config.go` (`Parse`). Doctor reports effective values in `internal/diagnostics/doctor.go`. Provider factory blocks HTTP requests when `Y_OFFLINE` is truthy. |
| SQLite session storage | Implemented in `internal/storage/sqlite_modernc.go` (build tag `feature_storage_sqlite`) with schema for `sessions` and `messages` tables. `sqlite_disabled.go` provides graceful error when tag is absent. Added to `feature_storage_sqlite` build flavor. |
| `y-pods shell <name>` | Implemented via `Manager.Shell()` in `pkg/pods/commands.go`. Uses `SSHClient.ExecStream` with `ForceTTY: true` for interactive shell. Wired in `cmd/y-pods/main.go`. |
| `y-pods logs <name>` | Implemented via `Manager.Logs()` in `pkg/pods/commands.go`. Uses `tail -n N` or `tail -F` over SSH `ExecStream`. Tests cover follow mode, line count, and model-not-found error. |
| `y-pods agent <name> [...]` | Implemented via `Manager.Agent()` in `pkg/pods/commands.go`. Sends chat completion requests to the vLLM HTTP endpoint (`/v1/chat/completions`) on the pod. Supports `--continue` flag. Wired in `cmd/y-pods/main.go`. |
| WASM provider host calls | `KindProviderRequest` already defined in `pkg/extensions/wasm/abi.go`. `dispatchProviderRequest` in `hostfuncs.go` correctly returns `CodeUnsupportedHostOp` for V1, matching the intentional non-goal. Test `TestDispatchHostCallProviderRequestUnsupported` verifies this behavior. |
| `pi-mom` legacy slash command catalogue | Command parity documented in `docs/y-mom.md` §"Command parity". Core routing (messages + `stop`) is implemented; niche commands are tracked for re-adding as Slack workspaces require them. |

## Process notes

- The TUI (`pkg/tui`) and web server (`pkg/web`, `cmd/y-web`) were
  removed in 2026-05-08 as the project is now SDK/CLI/JSON-RPC only.
- No file under `pi-mono`, `y-migration.md`, `extension-wasm.md`,
  `run-y-migration*.mjs`, or `y-state.json` was modified.
- Phase-0 and phase-7 sub-gap tables in `docs/baseline/gaps.md` remain
  the source of truth for *what was found during inventory*. This file
  is the source of truth for *what is still pending after the migration*
  and reflects the resolution status.
