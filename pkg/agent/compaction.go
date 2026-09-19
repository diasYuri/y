package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/diasYuri/y/pkg/agent/compaction"
	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/telemetry"
)

// maybeCompact checks the threshold at a checkpoint-safe turn boundary.
func (a *Agent) maybeCompact(ctx context.Context) {
	_, _ = a.compact(ctx, compaction.ReasonThreshold, false)
}

// compact is synchronous deliberately: returning from a run before its
// transcript rewrite completes would leave a recovery snapshot ambiguous.
// Force is used after a provider context-overflow response.
func (a *Agent) compact(ctx context.Context, reason compaction.Reason, force bool) (bool, error) {
	a.mu.Lock()
	if !a.compactionEnabled || a.compacting {
		a.mu.Unlock()
		return false, nil
	}
	provider := a.provider
	model := a.model
	transcript := cloneMessages(a.transcript)
	transcriptVersion := a.transcriptVersion
	compactor := a.compactor
	a.mu.Unlock()
	if provider == nil || (!force && model.ContextWindow <= 0) {
		return false, nil
	}
	if compactor == nil {
		compactor = compaction.NewCompactor()
	}

	tokens := compaction.AdjustedEstimate(compaction.EstimateTranscriptTokens(transcript), string(model.Provider))
	threshold := compactor.Threshold
	if threshold <= 0 {
		threshold = compaction.DefaultThreshold
	}
	if !force && tokens <= int64(float64(model.ContextWindow)*threshold) {
		return false, nil
	}

	a.mu.Lock()
	if a.compacting {
		a.mu.Unlock()
		return false, nil
	}
	a.compacting = true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.compacting = false; a.mu.Unlock() }()
	for _, hooks := range a.hooksSnapshot() {
		if hooks.BeforeCompaction != nil {
			if err := hooks.BeforeCompaction(ctx); err != nil {
				return false, fmt.Errorf("before compaction hook: %w", err)
			}
		}
	}

	compactCtx, span := a.startSpan(ctx, "compaction", telemetry.Attribute{Key: "model.id", Value: model.ID}, telemetry.Attribute{Key: "reason", Value: reason})
	defer span.End()
	a.emit(Event{Kind: EventCompactionStarted, State: StateStreaming})
	result, err := compactor.Compact(compactCtx, compaction.Request{Transcript: transcript, Provider: provider, Model: model, Reason: reason, Force: force})
	if err != nil {
		span.RecordError(err)
		a.emit(Event{Kind: EventCompactionCompleted, State: StateStreaming, Err: err})
		for _, hooks := range a.hooksSnapshot() {
			if hooks.AfterCompaction != nil {
				hooks.AfterCompaction(ctx, result, err)
			}
		}
		return false, err
	}
	if !result.Applied {
		span.SetStatus(telemetry.StatusOK, "not_applied")
		a.emit(Event{Kind: EventCompactionCompleted, State: StateStreaming})
		for _, hooks := range a.hooksSnapshot() {
			if hooks.AfterCompaction != nil {
				hooks.AfterCompaction(ctx, result, nil)
			}
		}
		return false, nil
	}
	payload, _ := json.Marshal(struct {
		Metadata         compaction.Metadata `json:"metadata"`
		ArchivedMessages []ai.Message        `json:"archived_messages,omitempty"`
	}{Metadata: result.Metadata, ArchivedMessages: result.ArchivedMessages})
	a.mu.Lock()
	if a.transcriptVersion == transcriptVersion {
		a.transcript = cloneMessages(result.Messages)
		a.transcriptVersion++
	}
	a.mu.Unlock()
	span.SetStatus(telemetry.StatusOK, "")
	a.recordAccounting(telemetry.Measurement{Dimensions: a.accountingDimensions(0, model, ""), Compactions: 1, EstimatedContextTokens: result.Metadata.TokensBefore})
	a.emit(Event{Kind: EventCompactionCompleted, State: StateStreaming, Payload: payload})
	for _, hooks := range a.hooksSnapshot() {
		if hooks.AfterCompaction != nil {
			hooks.AfterCompaction(ctx, result, nil)
		}
	}
	return true, nil
}
