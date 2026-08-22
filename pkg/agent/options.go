package agent

import (
	"time"

	"github.com/yuri/y/pkg/agent/compaction"
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

// WithModel sets the provider model used for future runs.
func WithModel(model ai.Model) Option {
	return func(a *Agent) {
		a.model = model
	}
}

// WithSystemPrompt sets the system prompt forwarded to the provider.
func WithSystemPrompt(systemPrompt string) Option {
	return func(a *Agent) {
		a.systemPrompt = systemPrompt
	}
}

// WithWorkspaceRoot sets the workspace root forwarded to tools.
func WithWorkspaceRoot(workspaceRoot string) Option {
	return func(a *Agent) {
		a.workspaceRoot = workspaceRoot
	}
}

// WithTranscript seeds the transcript used for the next run.
func WithTranscript(messages ...ai.Message) Option {
	return func(a *Agent) {
		a.transcript = append([]ai.Message(nil), messages...)
	}
}

// WithMaxTurns sets the maximum number of assistant/tool iterations.
func WithMaxTurns(maxTurns int) Option {
	return func(a *Agent) {
		a.maxTurns = maxTurns
	}
}

// WithToolExecutionMode sets the tool batch execution strategy.
func WithToolExecutionMode(mode ToolExecutionMode) Option {
	return func(a *Agent) {
		a.toolMode = mode
	}
}

// WithBeforeToolCall registers a hook called before each tool execution.
// If the hook returns an error, the tool is skipped and the error is reported.
func WithBeforeToolCall(hook ToolCallHook) Option {
	return func(a *Agent) {
		a.beforeToolCall = hook
	}
}

// WithAfterToolCall registers a hook called after each tool execution.
// The hook receives the tool call and response; errors are logged but not fatal.
func WithAfterToolCall(hook ToolCallHook) Option {
	return func(a *Agent) {
		a.afterToolCall = hook
	}
}

// WithSessionID sets the session identifier forwarded to providers for
// prompt-cache aware backends.
func WithSessionID(id string) Option {
	return func(a *Agent) {
		a.sessionID = id
	}
}

// WithThinkingBudgets sets per-level reasoning budgets. Providers that
// support reasoning use the budget for the selected ThinkingLevel.
func WithThinkingBudgets(budgets map[ai.ThinkingLevel]int64) Option {
	return func(a *Agent) {
		if budgets != nil {
			a.thinkingBudgets = budgets
		}
	}
}

// WithEventSink registers a callback that receives state-machine events.
func WithEventSink(sink EventSink) Option {
	return func(a *Agent) {
		a.onEvent = sink
	}
}

// WithCompaction enables transcript compaction when usage exceeds the
// configured threshold.
func WithCompaction(enabled bool) Option {
	return func(a *Agent) {
		a.compactionEnabled = enabled
	}
}

// WithCompactor sets a custom compactor. If nil, a default compactor is used.
func WithCompactor(c *compaction.Compactor) Option {
	return func(a *Agent) {
		a.compactor = c
	}
}

// WithBeforeRequest registers a hook called immediately before every provider
// Stream call. The hook may rewrite the outgoing request or short-circuit it
// by returning a non-nil [HookedResponse].
func WithBeforeRequest(hook BeforeRequestHook) Option {
	return func(a *Agent) {
		a.beforeRequest = hook
	}
}

// WithAfterRequest registers a hook called after every provider Stream call,
// including the synthetic short-circuited path. The hook may transform the
// returned error.
func WithAfterRequest(hook AfterRequestHook) Option {
	return func(a *Agent) {
		a.afterRequest = hook
	}
}

// WithOnError registers a hook called whenever the loop sees an error. The
// hook may classify the error and request a retry by returning [ErrRetry].
func WithOnError(hook ErrorHook) Option {
	return func(a *Agent) {
		a.onError = hook
	}
}

// WithStreamDefaults sets default [providers.StreamOptions] to merge into
// every request. Per-call overrides supplied via [RunOptions] take precedence
// over defaults; defaults take precedence over the agent's built-in fields
// (SessionID, ThinkingBudgets) only when they are non-zero.
func WithStreamDefaults(opts providers.StreamOptions) Option {
	return func(a *Agent) {
		a.streamDefaults = opts
	}
}

// WithToolConcurrency caps the number of tool calls executed in parallel
// when the agent runs in [ToolExecutionParallel] mode. Zero or negative
// values mean "unlimited" (the historical default).
func WithToolConcurrency(n int) Option {
	return func(a *Agent) {
		if n < 0 {
			n = 0
		}
		a.toolConcurrency = n
	}
}

// WithToolTimeout sets a per-call timeout applied to every tool execution.
// Zero means no per-call timeout (only the outer context controls duration).
//
// When a tool call exceeds the timeout, the agent records a tool-result
// error message in the transcript instead of failing the run. The model is
// then free to retry the same tool with the same arguments on the next turn,
// and only the global [WithMaxTurns] cap protects against an infinite
// timeout-retry loop. Callers that want stricter protection should pair
// WithToolTimeout with a small WithMaxTurns or implement de-duplication via
// [WithBeforeToolCall].
func WithToolTimeout(d time.Duration) Option {
	return func(a *Agent) {
		if d < 0 {
			d = 0
		}
		a.toolTimeout = d
	}
}

// WithMaxRetries sets the number of automatic retries applied to recoverable
// provider/tool errors. Defaults to 0 (no retries).
func WithMaxRetries(n int) Option {
	return func(a *Agent) {
		if n < 0 {
			n = 0
		}
		a.maxRetries = n
	}
}

// WithMaxRetryDelay caps the exponential backoff delay between retries.
// Defaults to 5s when retries are enabled.
func WithMaxRetryDelay(d time.Duration) Option {
	return func(a *Agent) {
		if d < 0 {
			d = 0
		}
		a.maxRetryDelay = d
	}
}

// WithLogger sets a logger used for non-fatal diagnostics (e.g., token
// estimate fallback, retry decisions). nil silences logging.
func WithLogger(l Logger) Option {
	return func(a *Agent) {
		a.logger = l
	}
}

// WithUsageObserver sets a callback invoked once per assistant turn with the
// observed [ai.Usage] and its [UsageOrigin] (reported vs estimated).
func WithUsageObserver(obs UsageObserver) Option {
	return func(a *Agent) {
		a.usageObserver = obs
	}
}
