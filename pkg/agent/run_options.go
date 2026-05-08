package agent

import (
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

// RunOptions are per-call overrides applied on top of the defaults
// configured at agent construction time. Zero-valued fields are ignored
// (they fall back to defaults). Per-call values take precedence over
// [WithStreamDefaults]; defaults take precedence over the agent's
// built-in SessionID and ThinkingBudgets when both are set.
type RunOptions struct {
	// Stream overrides specific [providers.StreamOptions] fields.
	// Only non-zero fields override; pointers and slices that are nil are ignored.
	Stream providers.StreamOptions

	// ExtraMessages, when non-empty, are appended to the transcript
	// before the first turn, after any messages passed positionally to
	// [Agent.RunMessages].
	ExtraMessages []ai.Message
}

// mergeStreamOptions merges three layers (built-in -> defaults -> per-call)
// in increasing precedence and returns the effective StreamOptions.
func mergeStreamOptions(builtin, defaults, perCall providers.StreamOptions) providers.StreamOptions {
	out := builtin
	out = overlayStreamOptions(out, defaults)
	out = overlayStreamOptions(out, perCall)
	return out
}

func overlayStreamOptions(base, top providers.StreamOptions) providers.StreamOptions {
	if top.Temperature != nil {
		base.Temperature = top.Temperature
	}
	if top.MaxTokens != 0 {
		base.MaxTokens = top.MaxTokens
	}
	if top.APIKey != "" {
		base.APIKey = top.APIKey
	}
	if top.Transport != "" {
		base.Transport = top.Transport
	}
	if top.CacheRetention != "" {
		base.CacheRetention = top.CacheRetention
	}
	if top.SessionID != "" {
		base.SessionID = top.SessionID
	}
	if len(top.Headers) > 0 {
		if base.Headers == nil {
			base.Headers = make(map[string]string, len(top.Headers))
		}
		for k, v := range top.Headers {
			base.Headers[k] = v
		}
	}
	if top.Timeout != 0 {
		base.Timeout = top.Timeout
	}
	if top.MaxRetries != 0 {
		base.MaxRetries = top.MaxRetries
	}
	if top.MaxRetryDelay != 0 {
		base.MaxRetryDelay = top.MaxRetryDelay
	}
	if top.Reasoning != "" {
		base.Reasoning = top.Reasoning
	}
	if len(top.ThinkingBudgets) > 0 {
		if base.ThinkingBudgets == nil {
			base.ThinkingBudgets = make(map[ai.ThinkingLevel]int64, len(top.ThinkingBudgets))
		}
		for k, v := range top.ThinkingBudgets {
			base.ThinkingBudgets[k] = v
		}
	}
	if len(top.Metadata) > 0 {
		base.Metadata = append([]byte(nil), top.Metadata...)
	}
	return base
}
