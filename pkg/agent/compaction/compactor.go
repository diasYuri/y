package compaction

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

// Summarizer is the minimal provider interface needed by the compactor.
type Summarizer interface {
	Stream(context.Context, providers.StreamRequest) (providers.EventStream, error)
}

// Compactor decides when to compact a transcript and performs the rewrite.
type Compactor struct {
	// Threshold is the fraction of the model's context window that must be
	// exceeded before compaction is triggered.  Defaults to 0.8.
	Threshold float64

	// KeepLast is the number of most recent messages to retain verbatim
	// after compaction.  Defaults to 6.
	KeepLast int
}

// DefaultThreshold is the default compaction threshold.
const DefaultThreshold = 0.8

// DefaultKeepLast is the default number of messages to keep after compaction.
const DefaultKeepLast = 6

// NewCompactor creates a Compactor with sensible defaults.
func NewCompactor() *Compactor {
	return &Compactor{
		Threshold: DefaultThreshold,
		KeepLast:  DefaultKeepLast,
	}
}

// MaybeCompact checks whether the transcript usage exceeds the threshold.  If
// so, it asks the summarizer to produce a summary and rewrites the transcript
// keeping the system prompt, the summary, and the last KeepLast messages.
func (c *Compactor) MaybeCompact(
	ctx context.Context,
	transcript []ai.Message,
	provider Summarizer,
	model ai.Model,
) ([]ai.Message, bool, error) {
	threshold := c.Threshold
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	keepLast := c.KeepLast
	if keepLast <= 0 {
		keepLast = DefaultKeepLast
	}

	if model.ContextWindow <= 0 {
		return append([]ai.Message(nil), transcript...), false, nil
	}

	tokens := EstimateTranscriptTokens(transcript)
	tokens = AdjustedEstimate(tokens, string(model.Provider))

	limit := int64(float64(model.ContextWindow) * threshold)
	if tokens <= limit {
		return append([]ai.Message(nil), transcript...), false, nil
	}

	summary, err := c.summarize(ctx, provider, model, transcript)
	if err != nil {
		return nil, false, fmt.Errorf("compaction summarization failed: %w", err)
	}

	rewritten := c.rewrite(transcript, summary, keepLast)
	return rewritten, true, nil
}

func (c *Compactor) summarize(
	ctx context.Context,
	provider Summarizer,
	model ai.Model,
	transcript []ai.Message,
) (string, error) {
	if provider == nil {
		return "", errors.New("summarizer is nil")
	}

	transcriptText := formatTranscript(transcript)

	req := providers.StreamRequest{
		Model: model,
		Context: ai.Context{
			SystemPrompt: SummarizationPrompt,
			Messages: []ai.Message{
				{
					Role:      ai.RoleUser,
					Timestamp: time.Now().UTC(),
					Content: []ai.ContentBlock{
						{Type: ai.ContentText, Text: transcriptText},
					},
				},
			},
		},
	}

	stream, err := provider.Stream(ctx, req)
	if err != nil {
		return "", err
	}
	defer stream.Close()

	var summary string
	for {
		event, err := stream.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
		switch e := event.(type) {
		case ai.TextDelta:
			summary += e.Text
		case ai.ErrorEvent:
			if e.Err != nil {
				return "", e.Err
			}
			return "", e
		case ai.StopEvent:
			if e.Reason == ai.StopReasonError {
				return "", errors.New("summary stream stopped with error")
			}
		}
	}

	return summary, nil
}

func (c *Compactor) rewrite(transcript []ai.Message, summary string, keepLast int) []ai.Message {
	if len(transcript) == 0 {
		return nil
	}

	// Find the first non-system message (the first user message).
	// We keep any system-like prefix messages, then insert the summary,
	// then keep the last keepLast messages.
	var result []ai.Message

	// Determine how many messages to preserve at the start.
	// We keep the first message if it looks like a system prompt setup,
	// otherwise we just insert the summary at the front.
	startIdx := 0
	if len(transcript) > 0 && transcript[0].Role == ai.RoleUser {
		// Keep the first user message as context anchor.
		result = append(result, cloneMessage(transcript[0]))
		startIdx = 1
	}

	// Insert summary message.
	result = append(result, ai.Message{
		Role:      ai.RoleUser,
		Timestamp: time.Now().UTC(),
		Content: []ai.ContentBlock{
			{Type: ai.ContentText, Text: "[Session summary]\n" + summary},
		},
	})

	// Append the last keepLast messages.
	if keepLast >= len(transcript) {
		// Not enough messages to drop any; just return the original
		// transcript unchanged to avoid losing everything.
		return append([]ai.Message(nil), transcript...)
	}

	cutoff := len(transcript) - keepLast
	if cutoff < startIdx {
		cutoff = startIdx
	}
	for i := cutoff; i < len(transcript); i++ {
		result = append(result, cloneMessage(transcript[i]))
	}

	return result
}

func formatTranscript(messages []ai.Message) string {
	var out string
	for _, msg := range messages {
		switch msg.Role {
		case ai.RoleUser:
			out += "User: " + contentText(msg.Content) + "\n"
		case ai.RoleAssistant:
			out += "Assistant: " + contentText(msg.Content) + "\n"
			for _, tc := range msg.ToolCalls {
				out += fmt.Sprintf("  ToolCall: %s(%s)\n", tc.Name, string(tc.Arguments))
			}
		case ai.RoleToolResult:
			if msg.ToolResult != nil {
				out += fmt.Sprintf("  ToolResult: %s = %s\n", msg.ToolResult.ToolName, contentText(msg.ToolResult.Content))
			}
		}
	}
	return out
}

func contentText(blocks []ai.ContentBlock) string {
	var out string
	for _, b := range blocks {
		if b.Type == ai.ContentText {
			out += b.Text
		}
	}
	return out
}

func cloneMessage(msg ai.Message) ai.Message {
	cloned := ai.Message{
		Role:      msg.Role,
		Timestamp: msg.Timestamp,
	}
	if len(msg.Content) > 0 {
		cloned.Content = make([]ai.ContentBlock, len(msg.Content))
		copy(cloned.Content, msg.Content)
		for i := range cloned.Content {
			if len(cloned.Content[i].ImageData) > 0 {
				cloned.Content[i].ImageData = append([]byte(nil), cloned.Content[i].ImageData...)
			}
			if len(cloned.Content[i].ProviderMetadata) > 0 {
				cloned.Content[i].ProviderMetadata = append([]byte(nil), cloned.Content[i].ProviderMetadata...)
			}
		}
	}
	if len(msg.ToolCalls) > 0 {
		cloned.ToolCalls = make([]ai.ToolCall, len(msg.ToolCalls))
		copy(cloned.ToolCalls, msg.ToolCalls)
		for i := range cloned.ToolCalls {
			if len(cloned.ToolCalls[i].Arguments) > 0 {
				cloned.ToolCalls[i].Arguments = append([]byte(nil), cloned.ToolCalls[i].Arguments...)
			}
		}
	}
	if msg.ToolResult != nil {
		cloned.ToolResult = &ai.ToolResult{
			ToolCallID: msg.ToolResult.ToolCallID,
			ToolName:   msg.ToolResult.ToolName,
			IsError:    msg.ToolResult.IsError,
			Details:    append([]byte(nil), msg.ToolResult.Details...),
		}
		if len(msg.ToolResult.Content) > 0 {
			cloned.ToolResult.Content = make([]ai.ContentBlock, len(msg.ToolResult.Content))
			copy(cloned.ToolResult.Content, msg.ToolResult.Content)
			for i := range cloned.ToolResult.Content {
				if len(cloned.ToolResult.Content[i].ImageData) > 0 {
					cloned.ToolResult.Content[i].ImageData = append([]byte(nil), cloned.ToolResult.Content[i].ImageData...)
				}
				if len(cloned.ToolResult.Content[i].ProviderMetadata) > 0 {
					cloned.ToolResult.Content[i].ProviderMetadata = append([]byte(nil), cloned.ToolResult.Content[i].ProviderMetadata...)
				}
			}
		}
	}
	return cloned
}
