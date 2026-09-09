package agent

import (
	"time"

	"github.com/yuri/y/pkg/agent/compaction"
	"github.com/yuri/y/pkg/ai"
	ycontext "github.com/yuri/y/pkg/context"
	"github.com/yuri/y/pkg/policy"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/telemetry"
)

// WithModel sets the provider model used for future runs.
func WithModel(model ai.Model) Option {
	return func(a *Agent) {
		a.model = model
	}
}

// WithRunID sets the durable execution ID used in emitted events and
// distributed snapshots. If omitted, Agent generates one when its first run
// starts.
func WithRunID(runID string) Option {
	return func(a *Agent) { a.runID = runID }
}

// WithRunNonce pins the execution nonce used to derive tool idempotency keys.
// Distributed runners use it to preserve the same operation identity while
// recovering a run on another worker. Ordinary persistent Agents generate a
// fresh nonce for each new run automatically.
func WithRunNonce(nonce string) Option {
	return func(a *Agent) { a.runNonce = nonce }
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

// WithContextResolver supplies request context from one or more replaceable
// sources. The resolved prompt is recomputed before each provider request.
func WithContextResolver(resolver *ycontext.Resolver) Option {
	return func(a *Agent) { a.contextResolver = resolver }
}

// WithContextRequest supplies tenant, project, session and token-budget
// identity to the context resolver.
func WithContextRequest(request ycontext.Request) Option {
	return func(a *Agent) { a.contextRequest = request }
}

// WithContextIdentity overlays request identity on an existing context
// request, preserving source-specific prompt and budget fields.
func WithContextIdentity(tenantID, workspaceID, projectID, sessionID, requestID string) Option {
	return func(a *Agent) {
		if tenantID != "" {
			a.contextRequest.TenantID = tenantID
		}
		if workspaceID != "" {
			a.contextRequest.WorkspaceID = workspaceID
		}
		if projectID != "" {
			a.contextRequest.ProjectID = projectID
		}
		if sessionID != "" {
			a.contextRequest.SessionID = sessionID
		}
		if requestID != "" {
			a.contextRequest.RequestID = requestID
		}
	}
}

// WithPolicyIdentity binds caller, tenant and capability identity to tool
// requests. It is persisted in neither provider snapshots nor event payloads
// beyond the non-secret identity fields.
func WithPolicyIdentity(identity policy.Identity) Option {
	identity.Capabilities = append([]string(nil), identity.Capabilities...)
	return func(a *Agent) { a.policyIdentity = identity }
}

// WithPolicyVersion records the policy contract used for authorization.
func WithPolicyVersion(version string) Option {
	return func(a *Agent) { a.policyVersion = version }
}

// WithAuthorizationExpiry bounds authorization for subsequent tool calls.
func WithAuthorizationExpiry(expiresAt time.Time) Option {
	return func(a *Agent) { a.authorizationExpiresAt = expiresAt }
}

// WithApprovalResolution supplies the durable decision used to resume a
// pending approval after a worker handoff.
func WithApprovalResolution(resolution *policy.ApprovalResolution) Option {
	return func(a *Agent) {
		if resolution == nil {
			a.approvalResolution = nil
			return
		}
		copy := *resolution
		a.approvalResolution = &copy
	}
}

// WithIdempotencyKey identifies a logical run request. The key is never
// included in a snapshot's secret-bearing provider options and is combined
// with each turn when constructing provider requests.
func WithIdempotencyKey(key string) Option {
	return func(a *Agent) { a.idempotencyKey = key }
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

// WithTracer attaches a transport-neutral tracer to run, provider, tool and
// compaction work. A nil tracer disables tracing.
func WithTracer(tracer telemetry.Tracer) Option {
	return func(a *Agent) { a.tracer = tracer }
}

// WithAccounting records execution metrics by run, turn, session, provider,
// model, tool and tenant. The collector remains optional for ephemeral runs.
func WithAccounting(accounting *telemetry.Accounting) Option {
	return func(a *Agent) { a.accounting = accounting }
}

// WithRuntimeHooks registers lifecycle hooks, resource providers and request
// tool providers. Hooks are appended in option order.
func WithRuntimeHooks(hooks RuntimeHooks) Option {
	return func(a *Agent) { a.runtimeHooks = append(a.runtimeHooks, hooks) }
}
