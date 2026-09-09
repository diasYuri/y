package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/policy"
	"github.com/yuri/y/pkg/telemetry"
	"github.com/yuri/y/pkg/tools"
)

func (a *Agent) executeToolCalls(ctx context.Context, toolCalls []ai.ToolCall, turn int) ([]ai.Message, error) {
	a.mu.Lock()
	registry := a.registry
	workspaceRoot := a.workspaceRoot
	mode := a.toolMode
	concurrency := a.toolConcurrency
	a.mu.Unlock()

	if registry == nil || len(toolCalls) == 0 {
		return nil, nil
	}

	a.setState(StateExecutingTools)
	preparedCalls := a.ensureToolCallIDs(toolCalls, turn)
	results := make([]ai.Message, len(preparedCalls))

	anySequential := mode == ToolExecutionSequential
	if !anySequential {
		for _, toolCall := range preparedCalls {
			if registry.GetExecutionMode(toolCall.Name) == tools.ExecutionSequential {
				anySequential = true
				break
			}
		}
	}

	if anySequential {
		for i, toolCall := range preparedCalls {
			message, err := a.executeToolCall(ctx, registry, workspaceRoot, toolCall, turn)
			if err != nil {
				a.markApprovalPending(err, toolCall, turn)
				return results[:i], err
			}
			results[i] = message
		}
		return results, nil
	}

	var semaphore chan struct{}
	if concurrency > 0 {
		semaphore = make(chan struct{}, concurrency)
	}
	var waitGroup sync.WaitGroup
	errorsCh := make(chan error, len(preparedCalls))
	for i, toolCall := range preparedCalls {
		waitGroup.Add(1)
		go func(index int, call ai.ToolCall) {
			defer waitGroup.Done()
			if semaphore != nil {
				select {
				case semaphore <- struct{}{}:
				case <-ctx.Done():
					errorsCh <- ctx.Err()
					return
				}
				defer func() { <-semaphore }()
			}

			message, err := a.executeToolCall(ctx, registry, workspaceRoot, call, turn)
			if err != nil {
				a.markApprovalPending(err, call, turn)
				errorsCh <- err
				return
			}
			results[index] = message
		}(i, toolCall)
	}
	waitGroup.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func (a *Agent) ensureToolCallIDs(toolCalls []ai.ToolCall, turn int) []ai.ToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	a.mu.Lock()
	runID := a.runID
	a.mu.Unlock()
	out := append([]ai.ToolCall(nil), toolCalls...)
	for i := range out {
		if out[i].ID == "" {
			out[i].ID = fmt.Sprintf("%s-turn-%d-tool-%d", runID, turn, i)
		}
	}
	return out
}

func (a *Agent) executeToolCall(ctx context.Context, registry ToolRegistry, workspaceRoot string, toolCall ai.ToolCall, turn int) (ai.Message, error) {
	if err := ctx.Err(); err != nil {
		return ai.Message{}, err
	}

	a.mu.Lock()
	toolTimeout := a.toolTimeout
	beforeToolCall := a.beforeToolCall
	afterToolCall := a.afterToolCall
	identity := a.policyIdentity
	policyVersion := a.policyVersion
	authorizationExpiresAt := a.authorizationExpiresAt
	var approval *policy.ApprovalResolution
	if a.approvalResolution != nil {
		copy := *a.approvalResolution
		approval = &copy
	}
	runID := a.runID
	runNonce := a.runNonce
	model := a.model
	a.mu.Unlock()
	toolCtx, toolSpan := a.startSpan(ctx, "tool.execution",
		telemetry.Attribute{Key: "tool.name", Value: toolCall.Name},
		telemetry.Attribute{Key: "tool.call_id", Value: toolCall.ID},
	)
	ctx = toolCtx
	defer toolSpan.End()
	toolStartedAt := time.Now()
	toolRequestID := fmt.Sprintf("%s-run-%s-turn-%d-tool-%s", runID, runNonce, turn, toolCall.ID)
	toolStartedEventKey := toolRequestID + ":started"
	toolEndedEventKey := toolRequestID + ":ended"
	for _, hooks := range a.hooksSnapshot() {
		if hooks.BeforeTool != nil {
			if err := hooks.BeforeTool(ctx, &toolCall); err != nil {
				message := toolResultMessage(toolCall, tools.ToolResponse{}, err)
				a.emit(Event{Kind: EventToolEnded, State: StateExecutingTools, Turn: turn, ToolCall: toolCall, ToolResult: *message.ToolResult, IdempotencyKey: toolEndedEventKey, Err: err})
				a.emit(Event{Kind: EventToolCompleted, State: StateExecutingTools, Turn: turn, ToolCall: toolCall, ToolResult: *message.ToolResult, IdempotencyKey: toolRequestID, Err: err})
				return message, nil
			}
		}
	}
	if toolTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, toolTimeout)
		defer cancel()
	}

	if beforeToolCall != nil {
		if err := beforeToolCall(ctx, toolCall, nil); err != nil {
			message := toolResultMessage(toolCall, tools.ToolResponse{}, err)
			a.emit(Event{Kind: EventToolEnded, State: StateExecutingTools, Turn: turn, ToolCall: toolCall, ToolResult: *message.ToolResult, IdempotencyKey: toolEndedEventKey, Err: err})
			a.emit(Event{Kind: EventToolCompleted, State: StateExecutingTools, Turn: turn, ToolCall: toolCall, ToolResult: *message.ToolResult, IdempotencyKey: toolRequestID, Err: err})
			return message, nil
		}
	}

	a.emit(Event{Kind: EventToolStarted, State: StateExecutingTools, Turn: turn, ToolCall: toolCall, IdempotencyKey: toolStartedEventKey})
	response, err := registry.Handle(ctx, tools.ToolRequest{
		ID:                     toolCall.ID,
		Name:                   toolCall.Name,
		Arguments:              toolCall.Arguments,
		WorkspaceRoot:          workspaceRoot,
		Identity:               identity,
		RequestID:              toolRequestID,
		RunID:                  runID,
		TurnID:                 fmt.Sprintf("%s-turn-%d", runID, turn),
		PolicyVersion:          policyVersion,
		AuthorizationExpiresAt: authorizationExpiresAt,
		Approval:               approval,
		IdempotencyKey:         toolRequestID,
		Progress: func(progress tools.ToolProgress) {
			if progress.ToolCallID == "" {
				progress.ToolCallID = toolCall.ID
			}
			if progress.ToolName == "" {
				progress.ToolName = toolCall.Name
			}
			a.emit(Event{Kind: EventToolProgress, State: StateExecutingTools, Turn: turn, ToolCall: toolCall, ToolProgress: progress})
		},
	})
	toolDuration := time.Since(toolStartedAt)
	measurement := telemetry.Measurement{Dimensions: a.accountingDimensions(turn, model, toolCall.Name), ToolDuration: toolDuration}
	if err != nil {
		toolSpan.RecordError(err)
	} else {
		toolSpan.SetStatus(telemetry.StatusOK, "")
	}
	a.recordAccounting(measurement)
	if err != nil {
		var approvalErr *tools.ApprovalError
		if errors.As(err, &approvalErr) {
			return ai.Message{}, err
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			// A per-call timeout becomes a tool-result message so the model can
			// decide whether to retry. Cancellation of the outer run still aborts.
			if toolTimeout > 0 && (errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)) {
				message := toolResultMessage(toolCall, tools.ToolResponse{}, err)
				a.emit(Event{Kind: EventToolEnded, State: StateExecutingTools, Turn: turn, ToolCall: toolCall, ToolResult: *message.ToolResult, IdempotencyKey: toolEndedEventKey, Err: err})
				a.emit(Event{Kind: EventToolCompleted, State: StateExecutingTools, Turn: turn, ToolCall: toolCall, ToolResult: *message.ToolResult, IdempotencyKey: toolRequestID, Err: err})
				if afterToolCall != nil {
					_ = afterToolCall(ctx, toolCall, message.ToolResult)
				}
				return message, nil
			}
			return ai.Message{}, err
		}

		if hookErr := a.invokeOnError(ctx, ErrorPhaseTool, err); hookErr != nil {
			err = hookErr
		}
		message := toolResultMessage(toolCall, tools.ToolResponse{}, err)
		a.emit(Event{Kind: EventToolEnded, State: StateExecutingTools, Turn: turn, ToolCall: toolCall, ToolResult: *message.ToolResult, IdempotencyKey: toolEndedEventKey, Err: err})
		a.emit(Event{Kind: EventToolCompleted, State: StateExecutingTools, Turn: turn, ToolCall: toolCall, ToolResult: *message.ToolResult, IdempotencyKey: toolRequestID, Err: err})
		if afterToolCall != nil {
			_ = afterToolCall(ctx, toolCall, message.ToolResult)
		}
		return message, nil
	}

	message := toolResultMessage(toolCall, response, nil)
	for _, hooks := range a.hooksSnapshot() {
		if hooks.AfterTool != nil {
			if hookErr := hooks.AfterTool(ctx, toolCall, message.ToolResult); hookErr != nil {
				message = toolResultMessage(toolCall, tools.ToolResponse{}, hookErr)
				break
			}
		}
	}
	a.emit(Event{Kind: EventToolEnded, State: StateExecutingTools, Turn: turn, ToolCall: toolCall, ToolResult: *message.ToolResult, IdempotencyKey: toolEndedEventKey})
	a.emit(Event{Kind: EventToolCompleted, State: StateExecutingTools, Turn: turn, ToolCall: toolCall, ToolResult: *message.ToolResult, IdempotencyKey: toolRequestID})
	if afterToolCall != nil {
		_ = afterToolCall(ctx, toolCall, message.ToolResult)
	}
	return message, nil
}

func (a *Agent) markApprovalPending(err error, toolCall ai.ToolCall, turn int) {
	var approvalErr *tools.ApprovalError
	if !errors.As(err, &approvalErr) || approvalErr == nil {
		return
	}
	request := approvalErr.Request
	if request.ToolCallID == "" {
		request.ToolCallID = toolCall.ID
	}
	a.mu.Lock()
	a.pendingApproval = &PendingApproval{Request: request, ToolCall: toolCall, Turn: turn}
	a.mu.Unlock()
}

func toolDescriptors(registry ToolRegistry) []ai.Tool {
	if registry == nil {
		return nil
	}
	descriptors := registry.List()
	if len(descriptors) == 0 {
		return nil
	}
	out := make([]ai.Tool, 0, len(descriptors))
	for _, descriptor := range descriptors {
		out = append(out, ai.Tool{
			Name:        descriptor.Name,
			Description: descriptor.Description,
			InputSchema: append([]byte(nil), descriptor.InputSchema...),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
