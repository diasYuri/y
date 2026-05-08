package compaction

import (
	"strings"
	"unicode"

	"github.com/yuri/y/pkg/ai"
)

// EstimateTokens returns a rough token count for a message using a
// chars-per-token heuristic. ASCII text is estimated at 4 characters per
// token. Non-ASCII runes are counted as one token each to account for
// multi-byte UTF-8 sequences that typically map to single tokens in modern
// tokenizers.
func EstimateTokens(msg ai.Message) int64 {
	var n int64
	for _, block := range msg.Content {
		n += estimateTextTokens(block.Text)
		n += estimateTextTokens(block.Thinking)
	}
	for _, tc := range msg.ToolCalls {
		n += estimateTextTokens(tc.Name)
		n += int64(len(tc.Arguments)) / 4
		if len(tc.Arguments)%4 != 0 {
			n++
		}
	}
	if msg.ToolResult != nil {
		n += estimateTextTokens(msg.ToolResult.ToolName)
		for _, block := range msg.ToolResult.Content {
			n += estimateTextTokens(block.Text)
		}
		n += int64(len(msg.ToolResult.Details)) / 4
		if len(msg.ToolResult.Details)%4 != 0 {
			n++
		}
	}
	return n
}

// EstimateTranscriptTokens sums token estimates for a slice of messages.
func EstimateTranscriptTokens(messages []ai.Message) int64 {
	var total int64
	for _, msg := range messages {
		total += EstimateTokens(msg)
	}
	return total
}

// ProviderAdjustment returns a multiplier for the given provider that
// tweaks the raw char/4 estimate toward that provider's tokenizer
// behaviour.  The default is 1.0 (no adjustment).
func ProviderAdjustment(providerID string) float64 {
	switch strings.ToLower(providerID) {
	case "openai":
		return 1.0 // tiktoken is close to 4 chars/token for English
	case "anthropic":
		return 0.95 // Claude tokenizer is slightly denser
	case "google":
		return 1.05 // Gemini tokenizer is slightly sparser
	default:
		return 1.0
	}
}

// AdjustedEstimate applies the provider-specific multiplier.
func AdjustedEstimate(tokens int64, providerID string) int64 {
	adj := ProviderAdjustment(providerID)
	return int64(float64(tokens) * adj)
}

func estimateTextTokens(text string) int64 {
	if text == "" {
		return 0
	}
	var asciiCount, nonASCIICount int64
	for _, r := range text {
		if r < unicode.MaxASCII {
			asciiCount++
		} else {
			nonASCIICount++
		}
	}
	tokens := asciiCount / 4
	if asciiCount%4 != 0 {
		tokens++
	}
	return tokens + nonASCIICount
}
