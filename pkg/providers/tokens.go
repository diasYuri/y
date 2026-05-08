package providers

import (
	"unicode/utf8"

	"github.com/yuri/y/pkg/ai"
)

// EstimateTokens returns a fast, model-agnostic token estimate for an
// ai.Context. Providers that do not expose a native token-count endpoint may
// use this as a fallback in CountTokens. The estimate uses a chars/4
// heuristic for text plus a flat per-image cost.
func EstimateTokens(c ai.Context) int64 {
	var chars int64
	if c.SystemPrompt != "" {
		chars += int64(utf8.RuneCountInString(c.SystemPrompt))
	}
	for _, msg := range c.Messages {
		for _, block := range msg.Content {
			switch block.Type {
			case ai.ContentText:
				chars += int64(utf8.RuneCountInString(block.Text))
			case ai.ContentThinking:
				chars += int64(utf8.RuneCountInString(block.Thinking))
			case ai.ContentImage:
				// Conservative fixed cost for an inline image.
				chars += 1024 * 4
			}
		}
		for _, call := range msg.ToolCalls {
			chars += int64(len(call.Name)) + int64(len(call.Arguments))
		}
		if msg.ToolResult != nil {
			for _, block := range msg.ToolResult.Content {
				chars += int64(utf8.RuneCountInString(block.Text))
			}
		}
	}
	for _, tool := range c.Tools {
		chars += int64(len(tool.Name)) + int64(len(tool.Description)) + int64(len(tool.InputSchema))
	}
	estimated := chars / 4
	if estimated < 1 && (chars > 0 || len(c.Messages) > 0) {
		estimated = 1
	}
	return estimated
}
