# WASM Extensions

`y` supports an opt-in WASM extension host built on
[wazero](https://github.com/tetratelabs/wazero). The host is gated behind
the `feature_wasm_ext` build tag so binaries compiled without it ship with
zero WASM code paths.

```bash
go build -tags feature_wasm_ext ./cmd/y
```

When the tag is absent, every Manager method that touches a guest
returns `wasm.ErrHostUnavailable` and the CLI surfaces the error
immediately — no silent fallback.

## ABI: pi.wasm.v1

The contract between host and guest is the JSON envelope described in
`extension-wasm.md` §10–§14. Guests must export these functions:

| Export                       | Signature                       | Purpose                       |
|------------------------------|---------------------------------|-------------------------------|
| `pi_extension_abi_version`   | `() -> i32`                     | Returns the supported ABI version. The host accepts version `1`. |
| `pi_extension_init`          | `(i32, i32) -> i64`             | Receives the JSON `init` envelope and reports tools/commands/providers. |
| `pi_extension_handle`        | `(i32, i32) -> i64`             | Services tool calls. |
| `pi_extension_shutdown`      | `(i32, i32) -> i64`             | Lets the guest release resources. |
| `pi_extension_free`          | `(i32, i32) -> ()`              | Releases buffers returned to the host. |
| `pi_extension_malloc` / `malloc` | `(i32) -> i32`              | Allocates guest memory for incoming envelopes. |

Returned `i64` values carry `(ptr, len)` packed as `ptr<<32 | len` so the
host knows where to read the JSON response.

## Host functions

The host registers a single module named `pi_host`. Its exports are:

| Function       | Signature                  | Purpose |
|----------------|----------------------------|---------|
| `pi_host_call` | `(i32, i32) -> i64`        | Generic dispatch. The guest sends a `HostCallRequest` envelope (`now`, `log`, `capability_info`, `tool_invoke`). |
| `pi_host_log`  | `(i32, i32, i32)`          | Optimised logging helper, gated by the `logs` capability and the per-call quota. |
| `pi_host_now`  | `() -> i64`                | Wall-clock millis. No capability required. |

Every dispatch goes through the host policy gate (see below) before any
side effect is emitted.

## Capabilities

Capabilities are deny-by-default. The manifest lists what the extension
*requests*, the host config lists what is *allowed*, and the runtime
policy can still revoke individual calls.

| Capability          | Granted by manifest key  |
|---------------------|-------------------------|
| `y_tools`           | `y_tools` / `pi_tools`  |
| `filesystem.read` / `filesystem.write` | `filesystem` |
| `network.http`      | `network`               |
| `process.exec`      | `process`               |
| `git.read` / `git.write` | `git`              |
| `secrets.read`      | `secrets`               |
| `storage`           | `storage`               |
| `logs`              | `logs`                  |

`wasm.ResolveCapabilityGrants` intersects manifest, allowlist and policy
to produce the runtime grant set. The set is exposed to guests via the
`capability_info` host call so they can fail fast instead of trapping
later.

## Limits

Each extension call runs under a `wasm.Limits` budget:

| Field            | Default     |
|------------------|-------------|
| `TimeoutMS`      | 5_000       |
| `MemoryPages`    | 256 (16 MiB)|
| `MaxInputBytes`  | 1 MiB       |
| `MaxOutputBytes` | 1 MiB       |
| `MaxLogBytes`    | 64 KiB      |
| `MaxHostCalls`   | 128         |

The runtime is configured with `WithCloseOnContextDone(true)`, so a
timeout cancels the wazero call and the host turns the resulting
`context.DeadlineExceeded` into `ExtensionError{Code: "timeout"}`. Traps
flow through the same path and surface as `Code: "trap"`. Neither tears
down the host process — `CallTool` always returns a structured error.

## Failure model

Errors raised to the caller are `*wasm.ExtensionError` with one of the
`Code…` constants exported from `pkg/extensions/wasm/errors.go`. Callers
can match on them with `errors.As` / `wasm.IsCode`:

```go
if wasm.IsCode(err, wasm.CodeCapabilityDenied) { … }
```

The `Retryable` flag is propagated from the guest response when the
guest itself returns an error.

## CLI

The `y extension` command tree is only registered in builds compiled
with `feature_wasm_ext`. Running it on a binary without the tag prints
`y: extension commands are unavailable in this build (missing
feature_wasm_ext)`.

| Command | Purpose |
|---------|---------|
| `y extension list [--dir <path>]` | Discovers manifests under the configured directories (default: `~/.pi/agent/extensions` and `./.y/extensions`) and prints a table with id, name, version, status and on-disk location. |
| `y extension info [--dir <path>] <id>` | Prints the full manifest plus runtime hints, declared capabilities and the resolved entry path for a single extension. |
| `y extension validate <path>` | Parses an `extension.toml` manifest (or the `extension.toml` inside a directory) without instantiating the wasm module. Returns a non-zero exit code on schema failures. |
| `y extension enable <id>` | Records `id = true` inside `~/.pi/agent/extensions.toml`. |
| `y extension disable <id>` | Records `id = false` in the same registry. |

`enable`/`disable` only update the toggle file; they do not load or
unload a running module. Loading happens lazily the first time the host
calls into the extension.

## Authoring quickstart

The smallest working extension lives in
[`examples/extensions/hello`](../examples/extensions/hello). It ships a
TinyGo source under `tinygo/main.go`, a `build.sh` that produces
`module.wasm`, and a regression test that does not depend on TinyGo.
Reuse the manifest, swap in your own tool implementation, and rebuild
with TinyGo to ship a custom extension.

The recommended workflow:

```bash
# 1. Validate the manifest as you edit it (no wasm needed yet).
y extension validate examples/extensions/hello

# 2. Build the wasm module.
./examples/extensions/hello/tinygo/build.sh

# 3. Discover the extension.
y extension list --dir examples/extensions

# 4. Inspect the resolved metadata.
y extension info --dir examples/extensions y.examples.hello
```

The `pkg/extensions/wasm/wasmtest` helper exposes a tiny WASM builder
used by the example's regression test. It is suitable for unit tests
that need an ABI-compliant module without invoking TinyGo.
