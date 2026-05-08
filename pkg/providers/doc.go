// Package providers defines provider-agnostic LLM streaming primitives,
// concrete provider implementations (Anthropic, OpenAI, Google, OpenAI-
// compatible), and reusable test helpers.
//
// # Concept
//
// A [Provider] streams a normalized [ai.Event] sequence for one provider
// family. Callers build a [StreamRequest] (model, [ai.Context], options),
// hand it to [Provider.Stream], and consume the resulting [EventStream]
// until io.EOF. Concrete providers ([anthropic], [openai], [google],
// [openai_compatible]) translate the request into the provider's wire
// format, perform the HTTPS POST, and translate the streamed response back
// into [ai.Event] values.
//
// # Lifecycle
//
//   - Construct a provider via the package-level New(...) function in each
//     concrete subpackage.
//   - Stream may be called concurrently; each call returns its own
//     EventStream that the caller must Close.
//   - Close on the Provider releases shared resources (idle HTTP
//     connections, OAuth refresh goroutines). Calling Close on a nil
//     receiver is a no-op; Close is idempotent.
//
// # Capabilities
//
// [Capabilities] reports the feature set a model is known to support
// (vision, tools, reasoning/thinking budgets, prompt cache, JSON mode,
// streaming). Unknown model IDs return zero-valued capabilities. Use this
// to gate features instead of hard-coding per-model checks.
//
// # Typed errors
//
// Network/HTTP failures are normalized into typed errors so callers can use
// [errors.As] to react to common conditions:
//
//   - [RateLimitError]: 429 from the upstream. RetryAfter (when set)
//     reflects the parsed Retry-After header in seconds or HTTP-date form.
//   - [AuthError]: 401/403 from the upstream. Treated as permanent.
//   - [ContextOverflowError]: 413 or upstream-reported context-window
//     overflow. Treated as permanent.
//   - [NetworkError]: transport failure (StatusCode == 0) or 5xx response.
//
// [ClassifyHTTPError] performs the mapping; concrete providers call it from
// their non-2xx response paths.
//
// # Streaming options
//
// [StreamOptions] holds cross-provider knobs (Temperature, MaxTokens,
// Headers, Reasoning, ThinkingBudgets, CacheRetention, etc.). API key
// resolution is uniform across providers:
//
//  1. StreamOptions.APIKey (per-request override) wins.
//  2. WithAPIKey constructor option wins next.
//  3. Provider env vars via the configured WithEnvLookup
//     (resolved through [pkg/providers/auth]).
//
// # Middleware, retry, request inspection
//
// Each provider exposes WithMiddleware to wrap its [http.RoundTripper]
// (composition is registration order: first registered → outermost). The
// helper [ApplyCommonClient] centralizes the wrapping logic. Per-provider
// retry policies are configured via WithRetryPolicy on the concrete
// provider; the default is [DefaultRetryPolicy]. WithRequestInspector
// installs a callback invoked with the fully-built [http.Request] just
// before it would be sent.
//
// # Dry-run
//
// WithDryRun on a concrete provider toggles dry-run mode: the provider
// builds and inspects the request but does not send it. Stream returns the
// [SyntheticDryRunStream], which emits a single StopEvent and EOF. This is
// useful for offline tests and audit logs.
//
// # Test helpers
//
// [FakeProvider] is an in-memory provider that returns queued
// [FakeResponse]s in FIFO order. Use it to drive agent-loop tests without
// touching the network. The canonical import path is
// [pkg/providers/providertest], which re-exports the same type.
//
// # Model lists
//
// Each concrete provider ships a curated model list generated from a JSON
// manifest. Run `go generate ./pkg/providers/...` to regenerate the
// CuratedModels lists after editing models.json. Models() in each provider
// fetches the upstream model list when an API key is available and falls
// back to the curated list on failure.
package providers
