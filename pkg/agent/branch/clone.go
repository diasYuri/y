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
	cloned := message
	cloned.Content = append([]ai.ContentBlock(nil), message.Content...)
	for i := range cloned.Content {
		cloned.Content[i].ImageData = append([]byte(nil), cloned.Content[i].ImageData...)
		cloned.Content[i].ProviderMetadata = append([]byte(nil), cloned.Content[i].ProviderMetadata...)
	}
	cloned.ToolCalls = append([]ai.ToolCall(nil), message.ToolCalls...)
	for i := range cloned.ToolCalls {
		cloned.ToolCalls[i].Arguments = append([]byte(nil), cloned.ToolCalls[i].Arguments...)
	}
	if message.ToolResult != nil {
		result := *message.ToolResult
		result.Details = append([]byte(nil), message.ToolResult.Details...)
		result.Content = append([]ai.ContentBlock(nil), message.ToolResult.Content...)
		for i := range result.Content {
			result.Content[i].ImageData = append([]byte(nil), result.Content[i].ImageData...)
			result.Content[i].ProviderMetadata = append([]byte(nil), result.Content[i].ProviderMetadata...)
		}
		cloned.ToolResult = &result
	}
	return cloned
}
