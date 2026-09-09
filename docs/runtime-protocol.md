# Runtime protocol and observability

`pkg/runtime` exposes a versioned, transport-neutral command envelope. The
same `runtime.Command` is valid over JSONL, HTTP, WebSocket, gRPC, NATS, and
Kafka bridges. A command handler owns authorization, idempotency, persistence,
and execution; adapters only decode and encode envelopes.

Use `runtime.NewCommand` to build an envelope and implement
`runtime.Handler` to dispatch it. Commands cover run lifecycle, messages,
steering, follow-ups, abort/retry/compaction, state/transcript/event reads,
model and thinking changes, fork/clone, and checkpoints. All responses carry
a schema version and a structured error when dispatch is rejected.

`runtime.JSONLAdapter`, `runtime.HTTPAdapter`, `runtime.WebSocketAdapter`,
and `runtime.RegisterGRPC` are directly usable transport adapters. The HTTP
adapter accepts `POST /v1/commands` and serves durable events with
`GET /v1/events?run_id=<id>&after=<sequence>` as SSE. `QueueAdapter` is the
SDK-independent NATS/Kafka bridge: map a broker delivery to `QueueMessage`,
call `Handle`, then acknowledge/commit only after it returns successfully.

Compaction is a synchronous safe-point operation. `compaction.Result` records
the boundary, token counts, archived messages, and tool-call/result ids, so a
store can preserve the dropped prefix as a branch or archive. Provider context
overflow triggers one forced compaction and a retry before the original error
is returned.

`telemetry.Tracer` is a neutral span contract. `telemetry.MemoryTracer` is a
concurrency-safe reference implementation. `telemetry.Accounting` aggregates
tokens, cost, latency, first-token time, retries, compactions, tool duration,
aborts, provider errors, and estimated context by run, turn, session,
provider, model, tool, and tenant.

WASM extensions can receive lifecycle boundaries without a TUI dependency.
`wasm.Manager.CallRuntime` dispatches `lifecycle`, `runtime_event`, and `hook`
envelopes through the established guest handle ABI; `wasm.RuntimeHooks` adapts
safe points directly to `agent.WithRuntimeHooks`.
