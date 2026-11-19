# Provider-neutral web search

SDK applications can implement `search.Provider` from `pkg/search` and expose
it to an agent with `tools.NewWebSearchTool` or
`tools.RegisterWebSearchProvider`. Search has its own public package because
it is an application capability separate from the model streaming contracts
in `pkg/providers`. Y defines no search transport, network client, vendor
integration, or default backend.

The provider receives a typed `search.Request` with a required query and
optional controls: allowed and excluded domains, location, result
`context_size` (`low`, `medium`, or `high`), a maximum result count, country,
language, publication-date range, and SafeSearch level. Domain filters match
the listed hostname and its subdomains case-insensitively; exclusions take
precedence when a host matches both lists. Country uses an ISO 3166-1 alpha-2
code and language uses an ISO 639-1 code; the tool normalizes them to uppercase
and lowercase respectively before calling the provider. `date_range` uses
inclusive publication dates in `YYYY-MM-DD` form and requires at least one
bound. A provider without a publication-date filter must not advertise that
capability. SafeSearch accepts `off`, `moderate`, and `strict`; an empty value
retains the provider's configured default. `context_size` means the amount of
supporting context returned with results; it never carries arbitrary prompt
text.

The provider's `Capabilities` declaration controls which optional fields
appear in the generated tool schema. A true capability means the backend can
apply the complete semantics of that control. A backend must enforce controls
it accepts and return `search.ErrUnsupportedControl` for unsupported controls;
it must not silently ignore them.

Results use `search.Response` and `search.Result`. Each result has a title,
absolute HTTP(S) URL, and snippet. It may also include `page_date` in
`YYYY-MM-DD` form and up to five additional `highlights` of at most 2,000 bytes
each. `page_date` is the provider's most relevant reported date and may reflect
publication or modification; it must not be presented as a publication date
unless the backend can distinguish those meanings. `metadata` is limited to
non-sensitive facts, including the provider identity when useful; credentials,
request headers, internal debug data, and raw provider payloads must not be
returned there. The adapter validates citation fields and bounds on evidence
and metadata before forwarding results.

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
