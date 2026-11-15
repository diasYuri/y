# Runtime composition

Y's CLI and RPC server build their standard composition through
`internal/runtime.BuildComposition`. That package is intentionally private: it
contains application defaults and adapters that depend on compiled build tags.
SDK applications should compose through the public contracts in `pkg/runtime`,
`pkg/component`, and the service packages they choose.

## Reproducing the composition pattern

`examples/component-harness` is a runnable SDK-side composition. It builds a
provider, tool registry, context source, and application harness from public
component definitions. The example deliberately owns its factories, so it can
be copied and adapted without importing `internal`:

```sh
go run ./examples/component-harness
```

The Y binary's feature-tagged tool registrations, filesystem defaults, and
application-specific extension setup are not exported as SDK defaults. For an
SDK application, declare the services it needs, bind them to component
instance IDs, and construct definitions with the public service keys. For
example, an alternate harness can omit context or replace the provider while
keeping the same lifecycle and dependency validation:

```go
harness, err := runtime.NewHarness(providerDefinition(), toolsDefinition())
if err != nil {
	return err
}
if err := harness.Start(ctx); err != nil {
	return err // Start rolls back scopes already started.
}
defer harness.Close(context.WithoutCancel(ctx))
```

`NewHarness` validates service versions, requirements, and provider bindings
before starting factories. Component scopes own registrations and resources;
startup failure closes acquired scopes in reverse dependency order. See
[`component-contract.md`](component-contract.md) and
[`examples/component-harness`](../examples/component-harness/README.md) for the
public contract and a complete implementation.

## Binary configuration

The application composition is selected with `components` and `bindings` in
the runtime config. Component IDs are instance IDs; `type` selects a built-in
adapter, `version` pins its contract version, and `settings` supplies adapter
configuration. Bindings name the provider instance for each required service.
Build-tagged capabilities are still validated against the compiled feature
catalog before any component starts. The legacy top-level feature/provider/tool
configuration remains supported and is translated to the equivalent default
component graph when explicit composition is absent.

The built-in catalog is application-owned and currently lives in
`internal/runtime`; SDK consumers can reproduce its graph and lifecycle through
the public contracts, but must provide their own component definitions and
factories. This keeps `pkg` independent from `internal` and makes application
defaults explicit at the integration boundary.
