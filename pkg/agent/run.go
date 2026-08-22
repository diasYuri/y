package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yuri/y/pkg/ai"
)

// Run appends a user prompt and executes the full agent loop.
func (a *Agent) Run(ctx context.Context, prompt string) (RunResult, error) {
	if prompt != "" {
		return a.RunMessages(ctx, ai.Message{
			Role:      ai.RoleUser,
			Content:   []ai.ContentBlock{{Type: ai.ContentText, Text: prompt}},
			Timestamp: time.Now().UTC(),
		})
	}
	return a.RunMessages(ctx)
}

// RunWithOptions is like [Agent.Run] but accepts per-call [RunOptions].
// Per-call StreamOptions take precedence over [WithStreamDefaults].
func (a *Agent) RunWithOptions(ctx context.Context, prompt string, opts RunOptions) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	messages := append([]ai.Message(nil), opts.ExtraMessages...)
	if prompt != "" {
		messages = append(messages, ai.Message{
			Role:      ai.RoleUser,
			Content:   []ai.ContentBlock{{Type: ai.ContentText, Text: prompt}},
			Timestamp: time.Now().UTC(),
		})
	}
	return a.RunMessages(withRunOptions(ctx, opts), messages...)
}

// RunMessages appends messages and executes the full agent loop.
func (a *Agent) RunMessages(ctx context.Context, messages ...ai.Message) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// An Agent owns one transcript and one Abort handle. Serializing runs keeps
	// those resources coherent and makes per-run options genuinely per-run.
	a.runMu.Lock()
	defer a.runMu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	a.abortMu.Lock()
	a.abortFunc = cancel
	a.abortMu.Unlock()
	defer func() {
		a.abortMu.Lock()
		a.abortFunc = nil
		a.abortMu.Unlock()
		cancel()
	}()

	a.appendTranscripts(messages)
	result := RunResult{State: StateIdle}

	model, err := a.resolveModel(runCtx)
	if err != nil {
		finalState := finalStateForError(runCtx, err)
		a.setState(finalState)
		return a.snapshotResult(result, finalState), err
	}
	result.Model = model

	turns := 0
	for {
		if err := runCtx.Err(); err != nil {
			a.setState(StateCanceled)
			return a.snapshotResult(result, StateCanceled), err
		}
		if turns >= a.maxTurns {
			a.setState(StateFailed)
			err := fmt.Errorf("agent exceeded max turns (%d)", a.maxTurns)
			return a.snapshotResult(result, StateFailed), err
		}

		a.injectSteering()
		a.emit(Event{Kind: EventTurnStarted, Turn: turns + 1, State: StateRequestingModel})

		assistant, usage, stopReason, err := a.requestAssistantWithRetry(runCtx, model, turns+1)
		if err != nil {
			if errors.Is(err, errBeforeRequestSwallowed) {
				continue
			}
			finalState := finalStateForError(runCtx, err)
			a.setState(finalState)
			result.Usage = addUsage(result.Usage, usage)
			a.recordRecoverableError(finalState, err)
			return a.snapshotResult(result, finalState), err
		}

		turns++
		result.Turns = turns
		result.Usage = addUsage(result.Usage, usage)
		result.StopReason = stopReason
		a.appendTranscript(assistant)
		a.emit(Event{Kind: EventTurnEnded, Turn: turns, State: StateStreaming, Message: assistant, Usage: usage})
		a.maybeCompact(runCtx)

		if len(assistant.ToolCalls) == 0 {
			result.StopReason = ai.StopReasonStop
			if a.injectPendingSteering() {
				continue
			}
			break
		}

		a.setState(StateExecutingTools)
		toolResults, err := a.executeToolCalls(runCtx, assistant.ToolCalls)
		if err != nil {
			finalState := finalStateForError(runCtx, err)
			a.setState(finalState)
			a.recordRecoverableError(finalState, err)
			return a.snapshotResult(result, finalState), err
		}
		a.appendTranscripts(toolResults)
	}

	a.appendFollowUps()
	a.setState(StateCompleted)
	a.emit(Event{Kind: EventCompleted, State: StateCompleted, Turn: turns})
	return a.snapshotResult(result, StateCompleted), nil
}

func (a *Agent) resolveModel(ctx context.Context) (ai.Model, error) {
	a.mu.Lock()
	if a.model.ID != "" {
		model := a.model
		a.mu.Unlock()
		return model, nil
	}
	provider := a.provider
	a.mu.Unlock()

	if provider == nil {
		return ai.Model{}, errors.New("agent provider is nil")
	}

	a.setState(StateSelectingModel)
	models, err := provider.Models(ctx)
	if err != nil {
		return ai.Model{}, err
	}
	if len(models) == 0 {
		return ai.Model{}, errors.New("provider returned no models")
	}

	model := models[0]
	a.mu.Lock()
	if a.model.ID == "" {
		a.model = model
	}
	a.mu.Unlock()
	return model, nil
}

func (a *Agent) injectSteering() {
	a.steeringMu.Lock()
	messages := a.steeringQueue
	a.steeringQueue = nil
	a.steeringMu.Unlock()
	a.appendTranscripts(messages)
}

func (a *Agent) injectPendingSteering() bool {
	a.steeringMu.Lock()
	messages := a.steeringQueue
	a.steeringQueue = nil
	a.steeringMu.Unlock()
	if len(messages) == 0 {
		return false
	}
	a.appendTranscripts(messages)
	return true
}

func (a *Agent) appendFollowUps() {
	a.followUpMu.Lock()
	messages := a.followUpQueue
	a.followUpQueue = nil
	a.followUpMu.Unlock()
	a.appendTranscripts(messages)
}

func (a *Agent) recordRecoverableError(state State, err error) {
	if state != StateFailed || !isRecoverable(err) {
		return
	}
	a.mu.Lock()
	a.recoverableErr = err
	a.mu.Unlock()
}

func (a *Agent) snapshotResult(result RunResult, state State) RunResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	result.Messages = cloneMessages(a.transcript)
	result.State = state
	result.Model = a.model
	return result
}
