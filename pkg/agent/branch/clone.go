package branch

import "github.com/yuri/y/pkg/ai"

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
	cloned := message
	cloned.ProviderMetadata = append([]byte(nil), message.ProviderMetadata...)
	cloned.Details = append([]byte(nil), message.Details...)
	if message.Error != nil {
		errorCopy := *message.Error
		cloned.Error = &errorCopy
	}
	cloned.Content = append([]ai.ContentBlock(nil), message.Content...)
	for i := range cloned.Content {
		cloned.Content[i].ImageData = append([]byte(nil), cloned.Content[i].ImageData...)
		cloned.Content[i].ProviderMetadata = append([]byte(nil), cloned.Content[i].ProviderMetadata...)
		cloned.Content[i].Details = append([]byte(nil), cloned.Content[i].Details...)
	}
	cloned.ToolCalls = append([]ai.ToolCall(nil), message.ToolCalls...)
	for i := range cloned.ToolCalls {
		cloned.ToolCalls[i].Arguments = append([]byte(nil), cloned.ToolCalls[i].Arguments...)
		cloned.ToolCalls[i].Details = append([]byte(nil), cloned.ToolCalls[i].Details...)
	}
	if message.ToolResult != nil {
		result := *message.ToolResult
		result.Details = append([]byte(nil), message.ToolResult.Details...)
		result.Content = append([]ai.ContentBlock(nil), message.ToolResult.Content...)
		for i := range result.Content {
			result.Content[i].ImageData = append([]byte(nil), result.Content[i].ImageData...)
			result.Content[i].ProviderMetadata = append([]byte(nil), result.Content[i].ProviderMetadata...)
			result.Content[i].Details = append([]byte(nil), result.Content[i].Details...)
		}
		result.Metadata = append([]byte(nil), message.ToolResult.Metadata...)
		result.Logs = append([]ai.LogEntry(nil), message.ToolResult.Logs...)
		for i := range result.Logs {
			result.Logs[i].Details = append([]byte(nil), result.Logs[i].Details...)
		}
		cloned.ToolResult = &result
	}
	return cloned
}
