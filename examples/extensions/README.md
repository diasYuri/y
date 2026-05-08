# y WASM extension examples

This directory hosts SDK examples that exercise the optional `pi.wasm.v1`
extension host introduced in Phase 8 of the Go migration. They are not
required to use y; they exist so authors have a copy/paste starting point
when shipping their own extensions.

| Example | Description |
|---------|-------------|
| [`hello`](./hello) | Minimal TinyGo extension that registers a single `hello_say` tool. Includes an end-to-end test. |

Every example expects to be loaded by a y binary built with the
`feature_wasm_ext` tag:

```bash
go build -tags feature_wasm_ext ./cmd/y
```

Without that tag the extension subcommands are gated off and any attempt
to load a guest module fails fast with `wasm.ErrHostUnavailable`. See
[`docs/wasm-extensions.md`](../../docs/wasm-extensions.md) for the
full host/guest contract.

## Pointing y at the examples

```bash
y extension list --dir examples/extensions
y extension info --dir examples/extensions y.examples.hello
y extension validate examples/extensions/hello
y extension enable y.examples.hello
y extension disable y.examples.hello
```

Enable/disable persist toggle state to `~/.pi/agent/extensions.toml`.
The list and info commands read manifests from disk and never
instantiate guest modules unless `module.wasm` is present.
