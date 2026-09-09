package agent

import (
	"context"
	"errors"

	"github.com/yuri/y/pkg/ai"
)

func addUsage(dst, src ai.Usage) ai.Usage {
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.ReasoningTokens += src.ReasoningTokens
	dst.CacheReadTokens += src.CacheReadTokens
	dst.CacheWriteTokens += src.CacheWriteTokens
	dst.TotalTokens += src.TotalTokens
	dst.Cost.Input += src.Cost.Input
	dst.Cost.Output += src.Cost.Output
	dst.Cost.CacheRead += src.Cost.CacheRead
	dst.Cost.CacheWrite += src.Cost.CacheWrite
	dst.Cost.Total += src.Cost.Total
	return dst
}

func finalStateForError(ctx context.Context, err error) State {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return StateCanceled
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); errors.Is(ctxErr, context.Canceled) || errors.Is(ctxErr, context.DeadlineExceeded) {
			return StateCanceled
		}
	}
	return StateFailed
}

func cloneMessages(messages []ai.Message) []ai.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]ai.Message, len(messages))
	for i, message := range messages {
		out[i] = cloneMessage(message)
	}
	return out
}

func cloneMessage(message ai.Message) ai.Message {
	if message.SchemaVersion == 0 {
		message.SchemaVersion = ai.CurrentSchemaVersion
	}
	cloned := ai.Message{
		SchemaVersion:    message.SchemaVersion,
		ID:               message.ID,
		Role:             message.Role,
		Timestamp:        message.Timestamp,
		ResponseID:       message.ResponseID,
		Provider:         message.Provider,
		ModelID:          message.ModelID,
		StopReason:       message.StopReason,
		Usage:            message.Usage,
		Details:          append([]byte(nil), message.Details...),
		ProviderMetadata: append([]byte(nil), message.ProviderMetadata...),
	}
	if message.Error != nil {
		errorCopy := *message.Error
		cloned.Error = &errorCopy
	}
	if len(message.Content) > 0 {
		cloned.Content = make([]ai.ContentBlock, len(message.Content))
		copy(cloned.Content, message.Content)
		for i := range cloned.Content {
			if len(cloned.Content[i].ImageData) > 0 {
				cloned.Content[i].ImageData = append([]byte(nil), cloned.Content[i].ImageData...)
			}
			if len(cloned.Content[i].ProviderMetadata) > 0 {
				cloned.Content[i].ProviderMetadata = append([]byte(nil), cloned.Content[i].ProviderMetadata...)
			}
			if len(cloned.Content[i].Details) > 0 {
				cloned.Content[i].Details = append([]byte(nil), cloned.Content[i].Details...)
			}
		}
	}
	if len(message.ToolCalls) > 0 {
		cloned.ToolCalls = make([]ai.ToolCall, len(message.ToolCalls))
		copy(cloned.ToolCalls, message.ToolCalls)
		for i := range cloned.ToolCalls {
			if len(cloned.ToolCalls[i].Arguments) > 0 {
				cloned.ToolCalls[i].Arguments = append([]byte(nil), cloned.ToolCalls[i].Arguments...)
			}
			if len(cloned.ToolCalls[i].Details) > 0 {
				cloned.ToolCalls[i].Details = append([]byte(nil), cloned.ToolCalls[i].Details...)
			}
		}
	}
	if message.ToolResult != nil {
		cloned.ToolResult = &ai.ToolResult{
			ToolCallID: message.ToolResult.ToolCallID,
			ToolName:   message.ToolResult.ToolName,
			IsError:    message.ToolResult.IsError,
			Details:    append([]byte(nil), message.ToolResult.Details...),
			Usage:      message.ToolResult.Usage,
			Metadata:   append([]byte(nil), message.ToolResult.Metadata...),
			Logs:       append([]ai.LogEntry(nil), message.ToolResult.Logs...),
		}
		for i := range cloned.ToolResult.Logs {
			cloned.ToolResult.Logs[i].Details = append([]byte(nil), cloned.ToolResult.Logs[i].Details...)
		}
		if len(message.ToolResult.Content) > 0 {
			cloned.ToolResult.Content = make([]ai.ContentBlock, len(message.ToolResult.Content))
			copy(cloned.ToolResult.Content, message.ToolResult.Content)
			for i := range cloned.ToolResult.Content {
				if len(cloned.ToolResult.Content[i].ImageData) > 0 {
					cloned.ToolResult.Content[i].ImageData = append([]byte(nil), cloned.ToolResult.Content[i].ImageData...)
				}
				if len(cloned.ToolResult.Content[i].ProviderMetadata) > 0 {
					cloned.ToolResult.Content[i].ProviderMetadata = append([]byte(nil), cloned.ToolResult.Content[i].ProviderMetadata...)
				}
				if len(cloned.ToolResult.Content[i].Details) > 0 {
					cloned.ToolResult.Content[i].Details = append([]byte(nil), cloned.ToolResult.Content[i].Details...)
				}
			}
		}
	}
	return cloned
}

func (a *Agent) appendTranscript(message ai.Message) {
	a.mu.Lock()
	a.transcript = append(a.transcript, cloneMessage(message))
	a.transcriptVersion++
	a.mu.Unlock()
}

func (a *Agent) appendTranscripts(messages []ai.Message) {
	if len(messages) == 0 {
		return
	}
	a.mu.Lock()
	a.transcript = append(a.transcript, cloneMessages(messages)...)
	a.transcriptVersion++
	a.mu.Unlock()
}
