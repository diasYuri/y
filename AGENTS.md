# Repository Guidelines

## Project Structure & Module Organization

This is a Go module (`github.com/diasYuri/y`) for the `y` runtime. The CLI entrypoint is in `cmd/y`; non-public application, configuration, storage, telemetry, and feature infrastructure belongs in `internal/`. Public-style APIs live under `pkg/`, including agent, AI, provider, tool, RPC, LSP, and optional WASM-extension packages. Shared fixtures are in `testdata/`, examples in `examples/`, documentation in `docs/`, and build/check helpers in `scripts/`. Keep public packages independent from top-level `internal` packages; `scripts/check-architecture.sh` enforces this boundary.

## Build, Test, and Development Commands

- `make check` — run formatting, lint, vet, default tests, and all feature-tagged tests.
- `make test` / `make test-all` — run `go test ./...` with default tags or every `feature_*` tag.
- `make build` — build the standard host binary; use `make build FLAVOR=minimal|full` for another profile.
- `make matrix` — cross-build the configured OS/architecture matrix.
- `make fmt`, `make lint`, `make vet` — run individual quality checks.
- `make models` — regenerate provider model files from `models.json`.

The runtime is intended to build with `CGO_ENABLED=0`; optional capabilities are selected through feature build tags.

## Coding Style & Naming Conventions

Use standard Go formatting (`gofmt`) with tabs for indentation and lowercase package names. Use mixedCaps for Go identifiers, descriptive exported names, and concise receiver names. Keep dependencies explicit, isolate side effects, preserve package boundaries, and return errors with useful context. Add package documentation in `doc.go` where the package has a public-facing API.

## Testing Guidelines

Place tests beside the code they exercise in `_test.go` files and name them `Test<Type>_<Behavior>` when a specific behavior needs to be clear. Prefer focused, deterministic tests and table-driven cases for variations. Run `make test` during development and `make test-all` when changing feature-gated code, providers, storage, or optional integrations.

## Commit & Pull Request Guidelines

Recent history uses concise Conventional Commit-style subjects such as `feat: Remove mom and pods products`. Use a short imperative subject with a type such as `feat`, `fix`, `test`, `docs`, or `chore`. Pull requests should explain the behavior and scope of the change, link relevant issues, call out build tags or configuration changes, and include the commands used for validation. Add screenshots only when changing user-facing output or documentation layout.
