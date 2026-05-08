// Package agent provides a provider-agnostic agent loop and state machine
// for orchestrating large-language-model (LLM) conversations that may invoke
// tools, span multiple turns, and need to be observed, snapshotted,
// compacted, branched, or steered while running.
//
// # Concept
//
// An [Agent] wraps a [Provider] (a streaming LLM) and a [ToolRegistry]
// (a catalog of executable tools). Calling [Agent.Run] (or the lower-level
// [Agent.RunMessages]) drives a loop:
//
//  1. The current transcript is sent to the provider.
//  2. The streaming response is consumed, emitting [Event]s for each
//     [EventKind] (text deltas, tool calls, usage updates, state changes).
//  3. If the assistant requested tool calls, every call is dispatched to
//     the [ToolRegistry] (sequentially or concurrently per
//     [ToolExecutionMode]) and its result is appended to the transcript.
//  4. The loop repeats until the model stops, the context is canceled,
//     [Agent.Abort] is called, or [Agent.MaxTurns] is exceeded.
//
// The transcript is owned by the agent and persists between [Agent.Run]
// calls so that follow-up prompts continue the conversation naturally.
//
// # Lifecycle states
//
// At any point [Agent.State] returns one of [State]:
//
//	StateIdle            -> not currently running
//	StateSelectingModel  -> resolving a model from the provider
//	StateRequestingModel -> sending a request and awaiting first event
//	StateStreaming       -> consuming streamed events from the provider
//	StateExecutingTools  -> dispatching tool calls
//	StateCompleted       -> last run finished cleanly
//	StateCanceled        -> last run was canceled (context or Abort)
//	StateFailed          -> last run failed (see [Agent.Recover] to retry)
//
// # Hooks
//
// Several hooks let callers observe and short-circuit the loop:
//
//   - [WithBeforeToolCall] / [WithAfterToolCall]: wrap individual tool
//     executions for tracing, policy enforcement, or result rewriting.
//   - [WithBeforeRequest] / [WithAfterRequest]: wrap each call to the
//     provider for caching, observability, or request shaping. A
//     BeforeRequest hook may return a synthetic [ai.Message] to short
//     circuit the request entirely (useful for prompt-cache hits).
//   - [WithOnError]: classify provider/tool errors and decide whether the
//     run should retry, fail, or transform the error.
//
// # Persisting and rehydrating sessions
//
// [Agent.Transcript] returns a snapshot copy of the current transcript and
// is the simplest way to persist a conversation. To capture additional
// in-memory state (model selection, session ID, run counters), use
// [Agent.Snapshot] which returns an [AgentSnapshot] suitable for
// JSON-encoding. Apply a snapshot to an existing agent with
// [Agent.Restore], or build a fresh agent with [RestoreAgent].
//
// Example:
//
//	snap := a.Snapshot()
//	data, _ := json.Marshal(snap)
//	// ... store data ...
//	var restored AgentSnapshot
//	_ = json.Unmarshal(data, &restored)
//	a2, err := RestoreAgent(provider, registry, restored)
//	if err != nil {
//	    // handle restore error (e.g. structurally invalid snapshot)
//	}
//
// # Compaction integration
//
// When a model defines a [ai.Model.ContextWindow], enabling
// [WithCompaction] causes the agent to estimate transcript size after each
// turn and asynchronously summarize older messages via the
// [compaction.Compactor] (see the sub-package compaction). Compaction is
// transparent: callers continue using [Agent.Run] without coordinating
// with the compactor.
//
// # Branches
//
// Long-running sessions may want to explore alternative tool-use paths or
// what-if questions. [BranchManager] (see branch.go) keeps multiple
// transcript variants that share a prefix and lets callers fork, merge,
// and prune them.
//
// # Provider integration
//
// The [Provider] interface intentionally captures only the subset of
// pkg/providers used by the loop. Any package implementing
// `Models` and `Stream` plus `ID` can drive the agent, including the fake
// provider used in tests. Cross-cutting provider settings (temperature,
// retries, transport, headers) are configured once with
// [WithStreamDefaults] and may be overridden per-call via [RunOptions].
//
// Example:
//
//	a := agent.New(provider, registry,
//	    agent.WithSystemPrompt("You are a helpful assistant."),
//	    agent.WithStreamDefaults(providers.StreamOptions{
//	        Temperature: ptr(0.2),
//	        MaxTokens:   2048,
//	    }),
//	)
//	res, err := a.Run(ctx, "summarize the README")
package agent
