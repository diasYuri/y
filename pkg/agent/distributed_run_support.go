package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/yuri/y/pkg/ai"
)

func (r *AgentRunner) loadInitial(ctx context.Context, request RunRequest) (Snapshot, uint64, error) {
	if request.InitialState != nil {
		state := normalizeSnapshot(*request.InitialState, request.RunID)
		var err error
		state, err = state.Migrate()
		if err != nil {
			return Snapshot{}, 0, err
		}
		if state.RunID != request.RunID {
			return Snapshot{}, 0, fmt.Errorf("%w: initial state run_id mismatch", ErrInvalidSnapshot)
		}
		if err := state.Validate(); err != nil {
			return Snapshot{}, 0, err
		}
		return state, state.Version, nil
	}
	state, err := r.loadState(ctx, request.RunID)
	if errors.Is(err, ErrStateNotFound) {
		return NewSnapshot(request.RunID, AgentSnapshot{SchemaVersion: CurrentSnapshotSchemaVersion, RunID: request.RunID}), 0, nil
	}
	if err != nil {
		return Snapshot{}, 0, err
	}
	state = normalizeSnapshot(state, request.RunID)
	state, err = state.Migrate()
	if err != nil {
		return Snapshot{}, 0, err
	}
	if err := r.hydrateTranscript(ctx, &state); err != nil {
		return Snapshot{}, 0, err
	}
	if err := state.Validate(); err != nil {
		return Snapshot{}, 0, err
	}
	return state, state.Version, nil
}

func (r *AgentRunner) hydrateTranscript(ctx context.Context, state *Snapshot) error {
	if r.transcriptStore == nil {
		return nil
	}
	messages, err := r.transcriptStore.LoadTranscript(ctx, state.RunID)
	if errors.Is(err, ErrTranscriptNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	state.Agent.Transcript = messages
	return nil
}

func (r *AgentRunner) monitorAbort(ctx context.Context, runID string, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(r.pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			control, err := r.control.GetControl(ctx, runID)
			if err != nil {
				continue
			}
			if control.Status == ControlRequested || control.Status == ControlObserved {
				if lifecycle, ok := r.control.(controlLifecycle); ok {
					_ = lifecycle.ObserveAbort(ctx, runID)
				}
				cancel()
				return
			}
		}
	}
}

func (r *AgentRunner) monitorLease(ctx context.Context, store LeaseStore, lease Lease, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := store.RenewLease(ctx, lease, 2*time.Minute); err != nil {
				cancel()
				return
			}
		}
	}
}

func (r *AgentRunner) lockFor(runID string) *sync.Mutex {
	r.runLocksMu.Lock()
	defer r.runLocksMu.Unlock()
	lock := r.runLocks[runID]
	if lock == nil {
		lock = &sync.Mutex{}
		r.runLocks[runID] = lock
	}
	return lock
}

func statusForResult(result RunResult, err error) RunStatus {
	if result.State == StateWaitingApproval || errors.Is(err, ErrApprovalPending) {
		return RunWaitingApproval
	}
	if result.State == StateCanceled || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return RunAborted
	}
	if err != nil {
		if result.State == StateFailed && isRecoverable(err) {
			return RunRecoverable
		}
		return RunFailed
	}
	return RunCompleted
}

func promptMessage(prompt string) ai.Message {
	if prompt == "" {
		return ai.Message{}
	}
	return ai.Message{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: prompt}}, Timestamp: time.Now().UTC()}
}
