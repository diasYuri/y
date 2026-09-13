# Y

Y is a Go runtime for agentic applications and coding workflows.

The main runtime must build without Node, Bun, TypeScript, Python, cgo, or
native dynamic plugins. Optional WASM extensions run on
[wazero](https://github.com/tetratelabs/wazero) when the binary is compiled
with `feature_wasm_ext`.

## Layout

- `cmd/y`: primary CLI entrypoint.
- `internal`: non-public infrastructure (compiled features, diagnostics,
  policy, logging, concrete storage, and build info).
- `pkg`: public-style packages for agent, AI types, providers, tools,
  optional config/session contracts, WASM extensions, and the optional
  secondary products.
- `docs`: runtime protocol and observability documentation.
- `examples/extensions`: TinyGo WASM extension example.
- `scripts`: measurement and build helper scripts.
- `testdata`: shared Go test fixtures.

## Documentation map

- [`docs/runtime-protocol.md`](docs/runtime-protocol.md) — runtime protocol,
  transports, observability, compaction, and WASM lifecycle integration.

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
cross-compilation, see the `make` targets and `scripts/` helpers.
