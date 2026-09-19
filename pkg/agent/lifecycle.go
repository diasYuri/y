package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/policy"
	"github.com/diasYuri/y/pkg/telemetry"
)

// invokeOnError calls the OnError hook (if any). When the hook returns nil,
// the loop is asked to swallow the error; the caller should treat the step as
// recoverable. When the hook returns [ErrRetry], the caller should retry the
// step. Any other return replaces the original error.
func (a *Agent) invokeOnError(ctx context.Context, phase ErrorPhase, err error) error {
	if err == nil {
		return nil
	}
	a.mu.Lock()
	hook := a.onError
	model := a.model
	a.mu.Unlock()
	if phase == ErrorPhaseRequest {
		a.recordAccounting(telemetry.Measurement{Dimensions: a.accountingDimensions(0, model, ""), ProviderErrors: 1})
	}
	for _, runtimeHook := range a.hooksSnapshot() {
		if runtimeHook.OnError == nil {
			continue
		}
		decision := runtimeHook.OnError(ctx, phase, err)
		if decision == nil || errors.Is(decision, ErrRetry) {
			return decision
		}
		err = decision
	}
	if hook != nil {
		return hook(ctx, phase, err)
	}
	return err
}

// State returns the agent's current lifecycle state.
func (a *Agent) State() State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

// Transcript returns a snapshot copy of the accumulated transcript.
func (a *Agent) Transcript() []ai.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneMessages(a.transcript)
}

// Reset replaces the transcript and returns the agent to the idle state.
func (a *Agent) Reset(messages ...ai.Message) {
	a.mu.Lock()
	a.transcript = cloneMessages(messages)
	a.transcriptVersion++
	a.state = StateIdle
	a.mu.Unlock()
}

// Continue resumes the agent loop from the current transcript without adding
// a new user message. A recoverable failure is routed through Recover.
func (a *Agent) Continue(ctx context.Context) (RunResult, error) {
	a.mu.Lock()
	state := a.state
	recoverable := a.recoverableErr
	a.mu.Unlock()
	if state == StateFailed && recoverable != nil {
		return a.Recover(ctx)
	}
	return a.RunMessages(ctx)
}

// Recover attempts to resume from StateFailed when the most recent failure
// was classified as recoverable.
func (a *Agent) Recover(ctx context.Context) (RunResult, error) {
	a.mu.Lock()
	if a.state != StateFailed {
		state := a.state
		a.mu.Unlock()
		return RunResult{}, fmt.Errorf("agent: cannot recover from state %q", state)
	}
	if a.recoverableErr == nil {
		a.mu.Unlock()
		return RunResult{}, errors.New("agent: last failure was not classified as recoverable")
	}
	a.recoverableErr = nil
	a.state = StateIdle
	a.mu.Unlock()
	return a.RunMessages(ctx)
}

// Steer injects messages into the current run. If the agent is idle, they are
// queued for the next run.
func (a *Agent) Steer(messages ...ai.Message) {
	a.steeringMu.Lock()
	defer a.steeringMu.Unlock()
	a.steeringQueue = append(a.steeringQueue, cloneMessages(messages)...)
}

// FollowUp queues messages for the next run after the current one completes.
func (a *Agent) FollowUp(messages ...ai.Message) {
	a.followUpMu.Lock()
	defer a.followUpMu.Unlock()
	a.followUpQueue = append(a.followUpQueue, cloneMessages(messages)...)
}

// Abort cancels the current run if one is in progress.
func (a *Agent) Abort() {
	a.abortMu.Lock()
	defer a.abortMu.Unlock()
	if a.abortFunc != nil {
		a.abortFunc()
		a.abortFunc = nil
		a.emit(Event{Kind: EventAbortRequested, State: StateCanceled})
		a.mu.Lock()
		model := a.model
		a.mu.Unlock()
		a.recordAccounting(telemetry.Measurement{Dimensions: a.accountingDimensions(0, model, ""), Aborts: 1})
	}
}

// ResolveApproval supplies the decision used when the next run resumes a
// pending approval. It is safe to call while the agent is idle between worker
// handoffs.
func (a *Agent) ResolveApproval(resolution policy.ApprovalResolution) {
	a.mu.Lock()
	copy := resolution
	a.approvalResolution = &copy
	a.mu.Unlock()
}

// Subscribe registers an additional event sink. The returned function is
// safe to call from any goroutine and is idempotent.
func (a *Agent) Subscribe(sink EventSink) (unsubscribe func()) {
	if sink == nil {
		return func() {}
	}
	a.sinksMu.Lock()
	a.nextSinkID++
	id := a.nextSinkID
	if a.sinks == nil {
		a.sinks = make(map[uint64]EventSink)
	}
	a.sinks[id] = sink
	a.sinksMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			a.sinksMu.Lock()
			delete(a.sinks, id)
			a.sinksMu.Unlock()
		})
	}
}
