# WASM components

WASM extensions can be activated through the component host. The persisted
`extensions.wasm` settings use comma-separated strings:

- `enabled_ids`: manifest IDs to activate.
- `directories`: directories scanned for `extension.toml` manifests.
- `allowed_capabilities`: host-granted capability names. An empty value grants
  none, regardless of what a manifest requests.

For example, a manifest can publish a JSON-mediated service and a tool:

```toml
id = "summarizer"
name = "Summarizer"
version = "1.2.0"
api_version = "y.wasm.v1"
entry = "summarizer.wasm"

[capabilities]
logs = true

[[provides]]
service_id = "summary.generate"
version = "1.0.0"

[[tools]]
name = "summarize"
description = "Summarize text"
input_schema = "{\"type\":\"object\",\"properties\":{\"text\":{\"type\":\"string\"}},\"required\":[\"text\"]}"
output_schema = "{\"type\":\"object\",\"properties\":{\"content\":{\"type\":\"array\"}},\"required\":[\"content\"]}"
```

Components can declare `[[requires]]` entries with `service_id`, `provider_id`,
and exact `version` fields. Providers expose only the typed `wasm.Service`
JSON bridge (`wasm.ServiceKey`); no Go object or guest memory crosses the
boundary. Service calls use the bounded runtime hook envelope.

`input_schema` is validated by the host tool registry. When `output_schema` is
present, the host validates the guest's structured `{"content":[...]}` tool
response before returning it. Validation uses the JSON Schema subset supported
by `pkg/tools.ValidateArguments`.

Tool and hook registrations are scoped to the active component generation.
Retiring a generation removes future dispatches, drains calls already admitted,
sends its stop lifecycle notification, and unloads the guest module. Builds
without `feature_wasm_ext` report an actionable error when `enabled_ids` is
non-empty.

The binary routes capability-bearing host calls through the harness policy as
well as the manifest/host grant intersection. Guest approval requests fail
closed; this adapter does not resume an interactive approval inside a guest
host call. A service requirement's `provider_id` is the full component ID,
for example `extensions/wasm.summarizer`. Selected WASM service graphs are
validated before component factories start.

An unload deadline stops waiting, leaving the module retiring. New calls are
rejected until a subsequent `Unload` or `Close` completes cleanup. Existing
runtime views retain their tools and module until their leases are released.
Shutdown runs once, and guest ABI dispatch is serialized per module. Each
module retains its configured memory ceiling regardless of loading order;
retiring modules still count toward `MaxLoadedModules`.

Runtime requirements are checked before guest resources are acquired.
`min_y_version` requires a known compatible host SemVer (the binary supplies its
build version). Instruction fuel metering is not available: nonzero `fuel`
settings fail explicitly. Wall-clock timeouts include time waiting for the
guest execution slot, and queued calls honor cancellation.
