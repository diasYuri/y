// Package compaction implements transcript compaction for the agent
// loop: when a session's estimated token usage approaches the model's
// context window, a [Compactor] asks the provider to summarize the older
// portion of the transcript and rewrites it to a shorter form that keeps
// the most recent messages verbatim.
//
// # When to customize
//
// The default [Compactor] (see [NewCompactor]) is appropriate for most
// chat-style sessions. Reach for a custom compactor when you need:
//
//   - A different threshold ([Compactor.Threshold]) — for example, a
//     more conservative 0.6 if your transcripts have heavy tool output
//     that the heuristic estimator under-counts.
//   - A different verbatim suffix size ([Compactor.KeepLast]) — for
//     example, a larger window when the agent relies on multi-turn tool
//     reasoning, or a smaller one for high-throughput summarization
//     pipelines.
//   - A different summarization prompt — set [SummarizationPrompt] or
//     wrap [Compactor] with your own helper before invoking
//     [Compactor.MaybeCompact] from a custom workflow.
//
// # How customization plugs into the agent
//
// The agent calls [Compactor.MaybeCompact] after each turn when
// compaction is enabled (see [github.com/yuri/y/pkg/agent.WithCompaction]).
// To swap in a custom compactor, pass it via
// [github.com/yuri/y/pkg/agent.WithCompactor]. The agent owns
// scheduling: compaction runs on a background goroutine and replaces the
// transcript only when the rewrite succeeds, so callers do not need to
// coordinate locks.
//
// # Contracts
//
// Implementations and overrides must respect:
//
//   - [Compactor.MaybeCompact] returns the original transcript and
//     `false` when compaction is not needed; in that case it must not
//     touch the provider.
//   - On a successful rewrite the returned slice is a new []ai.Message
//     (the agent assigns it directly). Callers must not retain pointers
//     into the input transcript.
//   - The summarizer ([Summarizer]) is invoked with a stream-style
//     request and is expected to behave like a normal provider call:
//     emit text deltas, terminate with a stop event, and honor
//     ctx.Done.
//   - Compaction must always preserve at least the leading user prompt
//     and the trailing window of [Compactor.KeepLast] messages; if the
//     transcript is too short to safely drop messages, it is returned
//     unchanged.
//
// # Token estimation
//
// [EstimateTokens] / [EstimateTranscriptTokens] use a fast heuristic
// (~4 ASCII characters per token, with provider-specific adjustments via
// [AdjustedEstimate]). They intentionally err on the side of triggering
// compaction earlier rather than later. Replace them in your own
// pipeline if you need exact tokenizer-based counts.
package compaction
