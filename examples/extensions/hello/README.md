# Hello WASM extension

This example registers a single tool, `hello_say`, that returns a greeting
for the supplied `name`. It is the smallest extension that exercises the
full `pi.wasm.v1` ABI — `pi_extension_init`, `pi_extension_handle`,
`pi_extension_shutdown`, plus the `malloc`/`pi_extension_free` pair that
the host uses to move JSON envelopes into and out of guest memory.

## Layout

```
examples/extensions/hello/
├── extension.toml      # manifest read by the y host
├── module.wasm         # produced by tinygo/build.sh (gitignored)
├── README.md
└── tinygo/
    ├── build.sh        # invokes `tinygo build -target=wasi`
    └── main.go         # TinyGo source for the extension
```

The `module.wasm` artefact is intentionally not checked in. Run
`tinygo/build.sh` (or any equivalent toolchain) to produce it. The
host-side regression test does not rely on the artefact being on disk —
see `hello_test.go`, which dynamically synthesises an ABI-compliant
module so CI keeps working without TinyGo installed.

## Building

```
./tinygo/build.sh
```

The script writes `module.wasm` next to the manifest. Once that file
exists you can drop the directory into `~/.pi/agent/extensions/` (or
point `y extension list --dir <path>` at the parent directory) and the
host will pick it up.

```
$ y extension list --dir examples/extensions
ID                     NAME    VERSION  STATUS       DIR
y.examples.hello       Hello   0.1.0    discovered   examples/extensions/hello
```

## Authoring guide

1. Declare the manifest. Mirror `extension-wasm.md §7`:
   - Set `api_version = "pi.wasm.v1"` so the host accepts the module.
   - List capabilities the extension actually uses; deny-by-default keeps
     the runtime surface tight.
   - Add a `[[tools]]` block per tool the guest plans to register at
     `pi_extension_init` time.
2. Implement the ABI. Each export reads a JSON envelope from guest
   memory, returns `(ptr<<32 | length)` for the response, and lets the
   host call `pi_extension_free` to release the buffer.
3. Keep allocations bounded. The host enforces `MaxInputBytes` and
   `MaxOutputBytes` quotas; clip your responses defensively to avoid
   surprising callers.
4. Surface failures as structured errors. Set `ok=false` and populate
   the `error` object instead of panicking; traps still surface as
   `wasm.CodeTrap` but cost the extension its retry budget.
5. Iterate quickly with `y extension validate ./examples/extensions/hello`
   — that hits only the manifest validation path and is safe to run
   without TinyGo installed.

For the larger ABI/capability/limits reference, see
[`docs/wasm-extensions.md`](../../../docs/wasm-extensions.md).
