package agent

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/tools"
)

func (a *Agent) executeToolCalls(ctx context.Context, toolCalls []ai.ToolCall) ([]ai.Message, error) {
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
	results := make([]ai.Message, len(toolCalls))

	anySequential := mode == ToolExecutionSequential
	if !anySequential {
		for _, toolCall := range toolCalls {
			if registry.GetExecutionMode(toolCall.Name) == tools.ExecutionSequential {
				anySequential = true
				break
			}
		}
	}

	if anySequential {
		for i, toolCall := range toolCalls {
			message, err := a.executeToolCall(ctx, registry, workspaceRoot, toolCall)
			if err != nil {
				return nil, err
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
	errorsCh := make(chan error, len(toolCalls))
	for i, toolCall := range toolCalls {
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

			message, err := a.executeToolCall(ctx, registry, workspaceRoot, call)
			if err != nil {
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
			return nil, err
		}
	}
	return results, nil
}

func (a *Agent) executeToolCall(ctx context.Context, registry ToolRegistry, workspaceRoot string, toolCall ai.ToolCall) (ai.Message, error) {
	if err := ctx.Err(); err != nil {
		return ai.Message{}, err
	}

	a.mu.Lock()
	toolTimeout := a.toolTimeout
	beforeToolCall := a.beforeToolCall
	afterToolCall := a.afterToolCall
	a.mu.Unlock()
	if toolTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, toolTimeout)
		defer cancel()
	}

	if beforeToolCall != nil {
		if err := beforeToolCall(ctx, toolCall, nil); err != nil {
			message := toolResultMessage(toolCall, tools.ToolResponse{}, err)
			a.emit(Event{Kind: EventToolEnded, State: StateExecutingTools, ToolCall: toolCall, ToolResult: *message.ToolResult, Err: err})
			return message, nil
		}
	}

	a.emit(Event{Kind: EventToolStarted, State: StateExecutingTools, ToolCall: toolCall})
	response, err := registry.Handle(ctx, tools.ToolRequest{
		ID:            toolCall.ID,
		Name:          toolCall.Name,
		Arguments:     toolCall.Arguments,
		WorkspaceRoot: workspaceRoot,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			// A per-call timeout becomes a tool-result message so the model can
			// decide whether to retry. Cancellation of the outer run still aborts.
			if toolTimeout > 0 && (errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)) {
				message := toolResultMessage(toolCall, tools.ToolResponse{}, err)
				a.emit(Event{Kind: EventToolEnded, State: StateExecutingTools, ToolCall: toolCall, ToolResult: *message.ToolResult, Err: err})
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
		a.emit(Event{Kind: EventToolEnded, State: StateExecutingTools, ToolCall: toolCall, ToolResult: *message.ToolResult, Err: err})
		if afterToolCall != nil {
			_ = afterToolCall(ctx, toolCall, message.ToolResult)
		}
		return message, nil
	}

	message := toolResultMessage(toolCall, response, nil)
	a.emit(Event{Kind: EventToolEnded, State: StateExecutingTools, ToolCall: toolCall, ToolResult: *message.ToolResult})
	if afterToolCall != nil {
		_ = afterToolCall(ctx, toolCall, message.ToolResult)
	}
	return message, nil
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
