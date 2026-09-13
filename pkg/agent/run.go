package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/policy"
	"github.com/yuri/y/pkg/telemetry"
	"github.com/yuri/y/pkg/tools"
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
	a.mu.Lock()
	if a.runID == "" {
		a.runID = newID()
	}
	if a.runNonce == "" {
		a.runNonce = newID()
	}
	runNonce := a.runNonce
	a.eventSequence = 0
	a.mu.Unlock()
	spanCtx, runSpan := a.startSpan(ctx, "agent.run", telemetry.Attribute{Key: "run.id", Value: a.runID})
	a.mu.Lock()
	pendingApproval := clonePendingApproval(a.pendingApproval)
	approvalResolution := cloneApprovalResolution(a.approvalResolution)
	a.mu.Unlock()
	a.emit(Event{Kind: EventAgentStarted, State: StateIdle})
	if err := a.notifySession(ctx, StateIdle); err != nil {
		a.setState(StateFailed)
		return a.snapshotResult(RunResult{State: StateFailed}, StateFailed), err
	}
	defer func() {
		a.mu.Lock()
		state := a.state
		a.mu.Unlock()
		a.emit(Event{Kind: EventAgentSettled, State: state})
		_ = a.notifySession(context.Background(), state)
		if state == StateCanceled {
			a.emit(Event{Kind: EventAbortCompleted, State: state})
		}
		a.mu.Lock()
		if a.runNonce == runNonce && state != StateWaitingApproval && (state == StateCompleted || state == StateCanceled || a.recoverableErr == nil) {
			a.runNonce = ""
		}
		a.mu.Unlock()
		if state == StateFailed || state == StateCanceled {
			runSpan.SetStatus(telemetry.StatusError, string(state))
		} else {
			runSpan.SetStatus(telemetry.StatusOK, "")
		}
		runSpan.End()
	}()
	if pendingApproval != nil {
		if pendingApproval.Request.ExpiresAt.After(time.Time{}) && time.Now().UTC().After(pendingApproval.Request.ExpiresAt) {
			a.setState(StateWaitingApproval)
			return a.snapshotResult(RunResult{State: StateWaitingApproval, Approval: &pendingApproval.Request}, StateWaitingApproval), ErrApprovalPending
		}
		if approvalResolution == nil {
			a.setState(StateWaitingApproval)
			return a.snapshotResult(RunResult{State: StateWaitingApproval, Approval: &pendingApproval.Request}, StateWaitingApproval), ErrApprovalPending
		}
		if approvalResolution.ApprovalID != "" && pendingApproval.Request.ApprovalID != "" && approvalResolution.ApprovalID != pendingApproval.Request.ApprovalID {
			return RunResult{State: StateWaitingApproval, Approval: &pendingApproval.Request}, fmt.Errorf("%w: approval ID does not match pending request", ErrApprovalPending)
		}
		if approvalResolution.State == "" || approvalResolution.State == policy.ApprovalPending ||
			(!approvalResolution.ExpiresAt.IsZero() && time.Now().UTC().After(approvalResolution.ExpiresAt)) {
			a.setState(StateWaitingApproval)
			return a.snapshotResult(RunResult{State: StateWaitingApproval, Approval: &pendingApproval.Request}, StateWaitingApproval), ErrApprovalPending
		}
		a.mu.Lock()
		a.pendingApproval = nil
		a.mu.Unlock()
	}

	runCtx, cancel := context.WithCancel(spanCtx)
	a.abortMu.Lock()
	a.abortFunc = cancel
	a.abortMu.Unlock()
	defer func() {
		a.abortMu.Lock()
		a.abortFunc = nil
		a.abortMu.Unlock()
		cancel()
	}()

	if pendingApproval == nil {
		for i := range messages {
			for _, hooks := range a.hooksSnapshot() {
				if hooks.BeforeMessage != nil {
					if err := hooks.BeforeMessage(runCtx, &messages[i]); err != nil {
						a.setState(StateFailed)
						return a.snapshotResult(RunResult{State: StateFailed}, StateFailed), err
					}
				}
			}
		}
		a.appendTranscripts(messages)
	}
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

		if pendingApproval != nil {
			a.setState(StateExecutingTools)
			a.mu.Lock()
			registry := a.registry
			workspaceRoot := a.workspaceRoot
			a.mu.Unlock()
			message, err := a.executeToolCall(runCtx, registry, workspaceRoot, pendingApproval.ToolCall, pendingApproval.Turn)
			if err != nil {
				var approvalErr *tools.ApprovalError
				if errors.As(err, &approvalErr) {
					a.markApprovalPending(err, pendingApproval.ToolCall, pendingApproval.Turn)
					a.setState(StateWaitingApproval)
					result.State = StateWaitingApproval
					result.Approval = &approvalErr.Request
					return a.snapshotResult(result, StateWaitingApproval), ErrApprovalPending
				}
				finalState := finalStateForError(runCtx, err)
				a.setState(finalState)
				a.recordRecoverableError(finalState, err)
				return a.snapshotResult(result, finalState), err
			}
			a.appendTranscript(message)
			a.emit(Event{Kind: EventTurnCompleted, Turn: pendingApproval.Turn, State: StateExecutingTools, ToolCall: pendingApproval.ToolCall})
			a.emit(Event{Kind: EventTurnEnded, Turn: pendingApproval.Turn, State: StateExecutingTools, ToolCall: pendingApproval.ToolCall})
			pendingApproval = nil
			continue
		}

		a.injectSteering()
		if err := a.applyBeforeTurn(runCtx, turns+1); err != nil {
			a.setState(StateFailed)
			return a.snapshotResult(result, StateFailed), err
		}
		a.emit(Event{Kind: EventMessageStarted, State: StateRequestingModel, Turn: turns + 1})
		a.emit(Event{Kind: EventTurnStarted, Turn: turns + 1, State: StateRequestingModel})

		assistant, usage, stopReason, err := a.requestAssistantWithRetry(runCtx, model, turns+1)
		if err != nil {
			if errors.Is(err, errBeforeRequestSwallowed) {
				continue
			}
			finalState := finalStateForError(runCtx, err)
			a.setState(finalState)
			result.Usage = addUsage(result.Usage, usage)
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				result.Error = &ai.ProviderError{
					Code:      "provider_request",
					Message:   err.Error(),
					Retryable: isTransient(err),
					Provider:  string(model.Provider),
					RequestID: fmt.Sprintf("%s-request-%d", a.runID, turns+1),
				}
			}
			a.recordRecoverableError(finalState, err)
			return a.snapshotResult(result, finalState), err
		}

		turns++
		result.Turns = turns
		result.Usage = addUsage(result.Usage, usage)
		result.StopReason = stopReason
		assistant.ToolCalls = a.ensureToolCallIDs(assistant.ToolCalls, turns)
		a.appendTranscript(assistant)
		a.maybeCompact(runCtx)

		if len(assistant.ToolCalls) == 0 {
			a.emit(Event{Kind: EventTurnCompleted, Turn: turns, State: StateStreaming, Message: assistant, Usage: usage})
			a.emit(Event{Kind: EventTurnEnded, Turn: turns, State: StateStreaming, Message: assistant, Usage: usage})
			if err := a.applyAfterTurn(runCtx, turns, result); err != nil {
				a.setState(StateFailed)
				return a.snapshotResult(result, StateFailed), err
			}
			result.StopReason = ai.StopReasonStop
			if a.injectPendingSteering() {
				continue
			}
			decision, err := a.applyBeforeComplete(runCtx, result)
			if err != nil {
				a.setState(StateFailed)
				return a.snapshotResult(result, StateFailed), err
			}
			if decision == CompletionContinue {
				continue
			}
			break
		}

		a.setState(StateExecutingTools)
		toolResults, err := a.executeToolCalls(runCtx, assistant.ToolCalls, turns)
		if err != nil {
			var approvalErr *tools.ApprovalError
			if errors.As(err, &approvalErr) {
				for _, toolResult := range toolResults {
					if toolResult.Role != "" {
						a.appendTranscript(toolResult)
					}
				}
				a.setState(StateWaitingApproval)
				result.State = StateWaitingApproval
				if approvalErr != nil {
					result.Approval = &approvalErr.Request
				}
				return a.snapshotResult(result, StateWaitingApproval), ErrApprovalPending
			}
			finalState := finalStateForError(runCtx, err)
			a.setState(finalState)
			a.recordRecoverableError(finalState, err)
			return a.snapshotResult(result, finalState), err
		}
		a.appendTranscripts(toolResults)
		// A turn containing tool calls is only durable after all tool results
		// have joined the transcript. Checkpointing before this point would
		// replay an assistant call without a way to reconcile its side effect.
		a.emit(Event{Kind: EventTurnCompleted, Turn: turns, State: StateExecutingTools, Message: assistant, Usage: usage})
		a.emit(Event{Kind: EventTurnEnded, Turn: turns, State: StateExecutingTools, Message: assistant, Usage: usage})
		if err := a.applyAfterTurn(runCtx, turns, result); err != nil {
			a.setState(StateFailed)
			return a.snapshotResult(result, StateFailed), err
		}
	}

	a.appendFollowUps()
	a.setState(StateCompleted)
	a.emit(Event{Kind: EventAgentCompleted, State: StateCompleted, Turn: turns})
	a.emit(Event{Kind: EventCompleted, State: StateCompleted, Turn: turns})
	return a.snapshotResult(result, StateCompleted), nil
}

func (a *Agent) resolveModel(ctx context.Context) (ai.Model, error) {
	a.mu.Lock()
	if a.model.ID != "" {
		model := a.model
		a.mu.Unlock()
		for _, hooks := range a.hooksSnapshot() {
			if hooks.BeforeModel != nil {
				if err := hooks.BeforeModel(ctx, &model); err != nil {
					return ai.Model{}, err
				}
			}
		}
		if err := a.applyAfterModel(ctx, model, nil); err != nil {
			return ai.Model{}, err
		}
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
	for _, hooks := range a.hooksSnapshot() {
		if hooks.BeforeModel != nil {
			if err := hooks.BeforeModel(ctx, &model); err != nil {
				return ai.Model{}, err
			}
		}
	}
	if err := a.applyAfterModel(ctx, model, nil); err != nil {
		return ai.Model{}, err
	}
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
	result.Model = sanitizeModel(a.model)
	return result
}
