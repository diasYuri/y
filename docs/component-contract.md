# Y component contract

The component contract is Y's public boundary for assembling harnesses. The
composition runtime lives in `pkg/component` and has no dependency on `agent`,
providers, tools, persistence, or an application package. A harness can use
those services or provide a different set of capabilities under the same
contract.

The static `Runtime` composes a fixed set of components. The dynamic `Manager`
reconciles desired definitions into committed generations between agent turns.
Both validate the graph before calling factories, activate providers before
consumers, and release tracked resources in reverse dependency order.

## Contract shape

A component is described by a `component.Descriptor` and a `component.Factory`.
The descriptor is available before construction and declares:

- a component ID and component version;
- legacy required service IDs and exact interface versions;
- service requirements with explicit provider IDs and SemVer constraints;
- provided service IDs and interface versions;
- requested capabilities for inspection by the application.

Component version and service interface version serve different purposes. A
provider can change its implementation version while preserving the service
contract its consumers require. Legacy requirements compare interface versions
exactly. Dynamic requirements select one provider explicitly and support exact
or bounded SemVer constraints. Multiple providers can be declared, but a
requirement without a provider selection is rejected. Applications that need
aggregation should expose a broker as the selected provider.

The `component.Key[T]` type gives a service a stable identity and a Go type.
Provider and consumer packages should share the same key declaration. The
descriptor uses the key's `ServiceRef`; `component.Provide`,
`component.Require`, and `component.Resolve` move the typed value through the
runtime. A component can only provide and require refs listed in its
descriptor. The runtime stores values behind private state; descriptors do
not expose mutable `agent.Agent` fields.

```go
var transcriptKey = component.NewKey[TranscriptStore]("y.transcript-store", "1")

descriptor := component.Descriptor{
    ID:      "app.transcripts",
    Version: "2.3.0",
    Provides: []component.ServiceRef{
        component.Reference(transcriptKey),
    },
}

definition := component.Define(descriptor, func() (component.Component, error) {
    return component.ComponentFunc(func(ctx context.Context, scope *component.Scope) error {
        store := newTranscriptStore()
        if err := scope.Track(func(context.Context) error { return store.Close() }); err != nil {
            return err
        }
        return component.Provide(scope, transcriptKey, store)
    }), nil
})
```

Factories are called only after validation and should be side-effect free.
Configuration can be captured in a closure; the component instance receives
the runtime scope only when it starts.

## Validation and lifecycle

`component.NewRuntime` validates a static graph before activation. It rejects
empty or duplicate declarations, duplicate component IDs, multiple providers
for one service ID, missing services, exact-version mismatches, and dependency
cycles. `component.NewManager` accepts multiple providers when consumers select
one explicitly, validates SemVer constraints, and detects cycles and
incompatible services before constructing components. Independent components
use component ID as a stable tie-break so activation order is deterministic.

The static runtime lifecycle is:

```text
new → starting → running → stopping → closed
             ↘ failed ───────────────→ closed
```

Each factory is invoked after graph validation. A component's `Start` receives
its declared service scope. It records every long-lived acquisition or
registration with `Scope.Track` and publishes declared services with
`component.Provide`. A declared provision must be installed before `Start`
returns successfully. Consumers start after their providers and can read only
declared requirements.

If construction or `Start` fails, the runtime disposes the failing component's
tracked resources, then rolls back already started components in reverse order.
Each scope runs cleanups in last-in, first-out order, attempts every cleanup,
and removes its service bindings only after its own cleanups finish. This keeps
provider services available while consumers and provider teardown run. The
original failure and cleanup failures are returned together.

`Close` is idempotent and invokes each cleanup at most once. Its context bounds
how long the caller waits; cleanup continues in the background with cancellation
and deadlines removed from the first close context. A later `Close` waits for
the same completion and returns the joined cleanup errors. The runtime is
single-use and cannot be restarted after it has failed or closed.

`Scope.Go` starts a worker with a scope-owned context. Scope close cancels and
waits for these workers before releasing tracked resources; non-cancellation
worker errors are joined with cleanup errors. Components can also use
`Scope.Track` for resources that provide their own lifecycle.

The manager lifecycle adds `inactive`, `starting`, `active`, `stopping`,
`failed`, and `closed` status for each component ID. A component whose required
provider is missing remains desired and inactive. Reconciliation stages new
generations before commit; an activation failure rolls back staged scopes and
leaves the previous committed set active. On commit, dependents stop before
providers, and their committed service bindings remain available during
teardown. Replacing a definition or selected provider starts a new generation.
`Manager.Reconcile` and `Manager.Close` accept contexts that bound caller
waiting while cleanup continues to completion.

`tools.Registry.Register`, `context.Resolver.Register`, and
`agent.RuntimeHookRegistry.Register` return owner handles. Closing a handle
removes it from future lookups and waits for calls or callbacks already in
progress. Context-source removal invalidates resolver caches. The component
`component.Manager.AcquireSnapshot` pins one committed component generation,
including its service bindings. `extensions.Host.AcquireRuntimeView` combines
that snapshot with the matching scoped tools, context sources, and hooks.
`Host.AgentOptions` installs this view source on an agent. The agent acquires a
view before the first turn and again at each turn boundary. A provider request,
its response, all resulting tool calls, context resolution, and turn hooks use
the same view. A tool advertised to the provider therefore dispatches through
the handler captured by that view.

Reconciliation can publish a newer generation while a turn is running. Existing
views keep their component scopes and registrations alive until their work
finishes; the next turn then captures the new generation. The first version
drains old work and does not cancel provider streams or tool calls. Direct
registry and resolver APIs retain their live semantics; agents composed with
`Host.AgentOptions` use operation snapshots.

Runtime view IDs are process-local and are included in run results, events, and
provider/tool spans, together with component generations, service bindings,
tool owners, hook owners, and context source origins. Context source versions
used by a request are recorded in the view diagnostics and provider span.
Runtime views and their callbacks are not serialized in `AgentSnapshot`. A
pending approval stores only the originating view ID when a managed runtime
view source is configured. If that ID does not match the view available on
resume, the agent records a synthetic tool error and asks the model for a fresh
tool call rather than executing an approval against a different handler
generation. Agents using only a process-local registry retain their existing
approval-resume behavior because those views cannot identify a managed
component generation.

`Scope.Track` and registration handles cannot prove that arbitrary component
code has no effects outside the scope. External emissions such as writing a
user file or sending a network request are outside rollback semantics, and
durable user data is preserved when its accessor is removed. See
[effects and durable state](effects-and-durable-state.md) for cleanup,
persistence, degradation and compensation contracts.

Capabilities are declared and inspectable here; enforcement must happen at the
service boundary that performs the operation. Declaring a dependency alone
does not grant every authority exposed by that service.

## Existing Y contracts

The component package does not import application-specific contracts. An
application can provide a `providers.Provider`, `*tools.Registry`,
`context.ContextSource`, `agent.RuntimeHooks`, or any other Go interface under
its own typed `component.Key[T]`. Concrete providers, tool packs, and context
sources can therefore be assembled without importing an app package.

`pkg/extensions.AdaptLegacyFactory` wraps an existing
`extensions.Extension` factory in a static component definition without
constructing it before composition validation. `AdaptLegacy` is a convenience
for an already-constructed extension. `Host.InstallContext` creates a scope per
extension, `Host.Uninstall` closes only the selected scope, and `Host.Reconcile`
replaces a desired extension set. Service-aware extensions implement
`ComponentDescriber`, `ScopedInstaller`, and `ExtensionFactory` so every
activated generation gets a fresh instance. Extensions that implement
`ScopedInstaller` use `Host.RegisterTool`, `RegisterSource`, and `RegisterHooks`
for transactional, owner-scoped contributions. The old
`Install`, `Registry.Add`, `AddSource`, and `AddHooks` APIs remain available for
host-lifetime composition; direct legacy mutations are not part of an
individual extension's rollback or unload.

The existing `Agent.Option`, `RuntimeHooks`, `tools.Registry`, `Provider`, and
`ContextSource` APIs remain available. The CLI headless runner and RPC server
now build through `internal/runtime.BuildComposition`, which adapts the binary's
compiled providers, tools, default policy, telemetry, memory, extension host,
and session store to the public `pkg/runtime.Harness`. The app package consumes
the resolved services and closes one harness, so startup rollback and shutdown
follow component dependency order. Legacy direct registrations remain
available for host-lifetime composition.

## Binary composition configuration

`pkg/config.Config` supports explicit component instances and service bindings.
Each component instance has a type, an optional exact implementation version,
an enabled flag, and component-owned settings. The `[bindings]` table selects
the instance for each runtime service. `pkg/config.GenerateDefault` emits a
fully pinned reference composition. If an existing config has no component or
binding tables, the binary adapts the legacy provider and memory settings to
that reference graph. Once either table is present, the explicit graph is
authoritative and legacy component settings are ignored.

```toml
[components.provider]
type = "provider.openai"
version = "1.0.0"
enabled = true

[components.policy]
type = "policy.default"
version = "1.0.0"
enabled = true

[components.telemetry]
type = "telemetry.default"
version = "1.0.0"
enabled = true

[components.tools]
type = "tools.default"
version = "1.0.0"
enabled = true

[bindings]
agent_provider = "provider"
policy_engine = "policy"
telemetry_emitter = "telemetry"
tools_registry = "tools"
```

Component types are checked against the binary's compiled capability registry.
Selecting a provider or tool capability omitted by its build tags fails
validation before its factory starts. Unselected capabilities do not prevent a
custom SDK harness from starting. The binary validates required service
bindings, their target component types, service versions, component versions,
the dependency graph, and component-specific settings before activation.
Sensitive tool operations still pass through the selected policy engine and
approval handler at the tool boundary; selecting a component does not itself
grant the authorities exposed by its services.

Configured WASM modules remain an extension-host setting under
`[extensions.wasm]`. The binary validates requested capability names and build
availability and preflights selected manifests before starting component
factories, then activates the selected modules inside the configured extension
host. Builds without the WASM tag can still use the harness when no WASM
modules are selected.

SDK consumers can assemble these public contracts through `pkg/component` and
`pkg/runtime` without importing `internal`. The binary's built-in providers,
tool packs, and storage adapters remain private to its composition package;
SDKs can provide their own factories under the same typed service keys.

| Existing surface | Component contract mapping in this stage |
| --- | --- |
| `providers.Provider` | Provide it under an application-owned `Key[providers.Provider]`; the owning component tracks `Provider.Close`. |
| `tools.Registry` / tool pack | Use `Host.RegisterTool` or `Registry.RegisterScoped` for generation-owned tools; direct `Register` and `Add` remain registry-lifetime. |
| `context.ContextSource` | Use `Host.RegisterSource` or `Resolver.RegisterScoped` for generation-owned sources; direct `Register` and `Add` remain resolver-lifetime. |
| `agent.RuntimeHooks` / `agent.Option` | Use `Host.RegisterHooks` or `RuntimeHookRegistry.RegisterScoped` for generation-owned hooks; ordinary `WithRuntimeHooks` remains agent-lifetime. |
| `extensions.Extension` | Use `Host.InstallContext`/`Host.Reconcile`; service-aware extensions implement `ComponentDescriber`, `ScopedInstaller`, and `ExtensionFactory`. The old `Install` method remains supported for legacy contributions. |
| `internal/feature.Registry` | Remains build-time application composition input; compiled components are selected by the application and described through `component.Definition`. |

## Vertical example

[`examples/component-harness`](../examples/component-harness) composes a
provider, tool registry, and context source, then uses them to build an
application harness. The example prints the dependency order before activation
and accesses the resulting harness through a typed key.

Run it from the repository root with:

```sh
go run ./examples/component-harness
```
