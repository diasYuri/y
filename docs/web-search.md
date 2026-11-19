# Provider-neutral web search

SDK applications can implement `search.Provider` from `pkg/search` and expose
it to an agent with `tools.NewWebSearchTool` or
`tools.RegisterWebSearchProvider`. Search has its own public package because
it is an application capability separate from the model streaming contracts
in `pkg/providers`. Y defines no search transport, network client, vendor
integration, or default backend.

The provider receives a typed `search.Request` with a required query and
common optional controls: allowed domains, location, result `context_size`
(`low`, `medium`, or `high`), and a maximum result count. `context_size` means
the amount of supporting context returned with results; it never carries
arbitrary prompt text. Its `Capabilities` declaration controls which optional
fields appear in the generated tool schema. A backend must enforce controls it
accepts and return `search.ErrUnsupportedControl` for unsupported controls; it
must not silently ignore them.

Results use `search.Response` and `search.Result`. Each result has a title,
absolute HTTP(S) URL, snippet, and optional string metadata. Metadata is
intended for non-sensitive facts only; credentials, request headers, internal
debug data, and raw provider payloads must not be returned there. The adapter
validates citation fields and bounded metadata before forwarding results.

The adapter registers `web_search` with the `web.search` policy capability and
marks it sensitive because queries can disclose user context to an external
backend. Registry policy and approval behavior apply as for other tools. The
registry-lifetime helper returns a `ToolRegistration` handle; component-owned
registration should build the descriptor and handler with `NewWebSearchTool`
and pass them to `extensions.Host.RegisterTool`, which ties removal and
in-flight draining to the component scope.

Providers must honor the supplied `context.Context` and preserve cancellation
and wrapped error causes. This contract does not claim that a backend supports
any optional control unless it declares that capability.
