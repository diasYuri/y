package agent

import (
	"context"

	"github.com/yuri/y/pkg/agent/compaction"
)

func (a *Agent) maybeCompact(ctx context.Context) {
	if !a.compactionEnabled {
		return
	}

	a.mu.Lock()
	if a.compacting {
		a.mu.Unlock()
		return
	}
	provider := a.provider
	model := a.model
	transcript := cloneMessages(a.transcript)
	transcriptVersion := a.transcriptVersion
	compactor := a.compactor
	a.mu.Unlock()

	if provider == nil || model.ContextWindow <= 0 {
		return
	}
	if compactor == nil {
		compactor = compaction.NewCompactor()
	}

	tokens := compaction.EstimateTranscriptTokens(transcript)
	tokens = compaction.AdjustedEstimate(tokens, string(model.Provider))
	limit := int64(float64(model.ContextWindow) * compactor.Threshold)
	if tokens <= limit {
		return
	}

	a.mu.Lock()
	if a.compacting {
		a.mu.Unlock()
		return
	}
	a.compacting = true
	a.mu.Unlock()

	go func() {
		defer func() {
			a.mu.Lock()
			a.compacting = false
			a.mu.Unlock()
		}()

		rewritten, didCompact, err := compactor.MaybeCompact(ctx, transcript, provider, model)
		if err != nil || !didCompact {
			return
		}

		// The run may have appended messages while compaction was in flight.
		// Do not replace a newer transcript with a rewrite of an old snapshot.
		a.mu.Lock()
		if a.transcriptVersion == transcriptVersion {
			a.transcript = cloneMessages(rewritten)
			a.transcriptVersion++
		}
		a.mu.Unlock()
	}()
}
