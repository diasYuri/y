# y

Go migration workspace for the `y` runtime.

This repository is the target of the `pi-mono` to `y` migration. The
main runtime is Go and must build without Node, Bun, TypeScript, Python,
cgo, or native dynamic plugins. Optional WASM extensions run on
[wazero](https://github.com/tetratelabs/wazero) only when the binary was
compiled with `feature_wasm_ext`.

## Layout

- `cmd/y`: primary CLI entrypoint.
- `cmd/y-mom`: optional Slack automation product (`feature_mom`).
- `cmd/y-pods`: optional GPU pod / vLLM management product (`feature_pods`).
- `internal`: non-public infrastructure (config, features, diagnostics,
  policy, logging, storage, build info).
- `pkg`: public-style packages for agent, AI types, providers, tools,
  WASM extensions, and the optional secondary products.
- `docs`: release, migration, baseline, performance, and feature docs.
- `examples/extensions`: TinyGo WASM extension example.
- `scripts`: measurement and build helper scripts.
- `testdata`: shared Go test fixtures.

## Documentation map

- `docs/release.md` — build profiles, install, configuration, providers,
  diagnostics, and release artefacts.
- `docs/migration-from-pi.md` — step-by-step cutover from `pi-mono` to
  `y` for operators.
- `docs/providers.md` — provider auth and HTTP details.
- `docs/run-chat.md` — `y run` and `y chat` headless surface.
- `docs/sessions.md` — on-disk session format.
- `docs/git-workflows.md` — git-tool safety rules.
- `docs/wasm-extensions.md` — extension host, ABI, capabilities, and CLI.
- `docs/y-mom.md` — secondary product docs.
- `docs/performance/memory-hardening.md` — hot-path memory guidance.
- `docs/baseline/` — pi-mono inventory, behaviour matrix, gaps, and
  benchmark plan.

## Quick start

```bash
# Run the test suite.
go test ./...

# Build the primary binary without cgo.
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" ./cmd/y

# Inspect the binary.
./y --version
./y features
./y doctor
```

For build profiles (`y-minimal`, `y-standard`, `y-full`) and
cross-compilation, see `docs/release.md`. For an operator-focused upgrade
walkthrough, see `docs/migration-from-pi.md`.

## Status

The migration is at phase 9 (cutover): every primary phase from baseline
through WASM extensions has shipped, and the release / migration docs are
the next gate before final verification. Phase progress and gaps are
tracked in `y-state.json` and `docs/baseline/gaps.md`.
