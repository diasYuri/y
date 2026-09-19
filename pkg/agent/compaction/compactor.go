package compaction

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/providers"
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

// Reason identifies why a compaction was requested. Callers may force an
// overflow compaction after a provider rejects an otherwise valid request.
type Reason string

const (
	ReasonThreshold Reason = "threshold"
	ReasonOverflow  Reason = "overflow"
	ReasonManual    Reason = "manual"
)

// Request is a storage-neutral compaction request. Transcript may originate
// from memory, an event-sourced tree, or any persisted session backend.
type Request struct {
	Transcript     []ai.Message
	Provider       Summarizer
	Model          ai.Model
	Reason         Reason
	Force          bool
	BranchSummary  string
	FileOperations []FileOperation
}

// FileOperation records a caller-observed file effect relevant to a summary.
// Compaction never inspects a filesystem; adapters may attach these records
// when tool execution makes that information available.
type FileOperation struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

// Metadata makes a compaction replayable and auditable without imposing a
// storage format. ArchivedMessages are the exact dropped prefix, including
// tool calls and results; a store may keep them as a branch or cold archive.
type Metadata struct {
	Reason             Reason          `json:"reason"`
	TokensBefore       int64           `json:"tokens_before"`
	TokensAfter        int64           `json:"tokens_after"`
	Boundary           int             `json:"boundary"`
	ArchivedMessageIDs []string        `json:"archived_message_ids,omitempty"`
	ToolCallIDs        []string        `json:"tool_call_ids,omitempty"`
	ToolResultCallIDs  []string        `json:"tool_result_call_ids,omitempty"`
	BranchSummary      string          `json:"branch_summary,omitempty"`
	FileOperations     []FileOperation `json:"file_operations,omitempty"`
}

// Result contains both the live transcript and the information a durable
// store needs to recover the exact pre-compaction history.
type Result struct {
	Messages         []ai.Message `json:"messages"`
	ArchivedMessages []ai.Message `json:"archived_messages,omitempty"`
	Applied          bool         `json:"applied"`
	Metadata         Metadata     `json:"metadata"`
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
	result, err := c.Compact(ctx, Request{Transcript: transcript, Provider: provider, Model: model, Reason: ReasonThreshold})
	return result.Messages, result.Applied, err
}

// Compact applies a recoverable rewrite. It never mutates the supplied
// transcript. When Force is true (normally after a ContextOverflowError), it
// bypasses the threshold but still refuses to rewrite a transcript that is
// already smaller than the verbatim retention window.
func (c *Compactor) Compact(ctx context.Context, request Request) (Result, error) {
	transcript := request.Transcript
	result := Result{Messages: cloneMessages(transcript)}
	reason := request.Reason
	if reason == "" {
		reason = ReasonThreshold
	}
	result.Metadata.Reason = reason
	threshold := c.Threshold
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	keepLast := c.KeepLast
	if keepLast <= 0 {
		keepLast = DefaultKeepLast
	}

	if request.Model.ContextWindow <= 0 && !request.Force {
		return result, nil
	}

	tokens := EstimateTranscriptTokens(transcript)
	tokens = AdjustedEstimate(tokens, string(request.Model.Provider))
	result.Metadata.TokensBefore = tokens

	limit := int64(float64(request.Model.ContextWindow) * threshold)
	if !request.Force && tokens <= limit {
		return result, nil
	}
	if len(transcript) <= keepLast {
		return result, nil
	}

	summary, err := c.summarize(ctx, request.Provider, request.Model, transcript)
	if err != nil {
		return Result{}, fmt.Errorf("compaction summarization failed: %w", err)
	}

	rewritten, boundary := c.rewriteWithBoundary(transcript, summary, keepLast)
	if len(rewritten) == len(transcript) {
		return result, nil
	}
	result.Messages = rewritten
	result.Applied = true
	result.Metadata.Boundary = boundary
	result.Metadata.TokensAfter = AdjustedEstimate(EstimateTranscriptTokens(rewritten), string(request.Model.Provider))
	result.Metadata.BranchSummary = request.BranchSummary
	result.Metadata.FileOperations = append([]FileOperation(nil), request.FileOperations...)
	result.ArchivedMessages = cloneMessages(transcript[:boundary])
	for _, message := range result.ArchivedMessages {
		if message.ID != "" {
			result.Metadata.ArchivedMessageIDs = append(result.Metadata.ArchivedMessageIDs, message.ID)
		}
		for _, call := range message.ToolCalls {
			if call.ID != "" {
				result.Metadata.ToolCallIDs = append(result.Metadata.ToolCallIDs, call.ID)
			}
		}
		if message.ToolResult != nil && message.ToolResult.ToolCallID != "" {
			result.Metadata.ToolResultCallIDs = append(result.Metadata.ToolResultCallIDs, message.ToolResult.ToolCallID)
		}
	}
	return result, nil
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
	defer func() { _ = stream.Close() }()

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
	result, _ := c.rewriteWithBoundary(transcript, summary, keepLast)
	return result
}

func (c *Compactor) rewriteWithBoundary(transcript []ai.Message, summary string, keepLast int) ([]ai.Message, int) {
	if len(transcript) == 0 {
		return nil, 0
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
		return cloneMessages(transcript), 0
	}

	cutoff := len(transcript) - keepLast
	if cutoff < startIdx {
		cutoff = startIdx
	}
	for i := cutoff; i < len(transcript); i++ {
		result = append(result, cloneMessage(transcript[i]))
	}

	return result, cutoff
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
	if msg.SchemaVersion == 0 {
		msg.SchemaVersion = ai.CurrentSchemaVersion
	}
	cloned := ai.Message{
		SchemaVersion:    msg.SchemaVersion,
		ID:               msg.ID,
		Role:             msg.Role,
		Timestamp:        msg.Timestamp,
		ResponseID:       msg.ResponseID,
		Provider:         msg.Provider,
		ModelID:          msg.ModelID,
		StopReason:       msg.StopReason,
		Usage:            msg.Usage,
		StructuredOutput: append([]byte(nil), msg.StructuredOutput...),
		Details:          append([]byte(nil), msg.Details...),
		ProviderMetadata: append([]byte(nil), msg.ProviderMetadata...),
	}
	if msg.Error != nil {
		errorCopy := *msg.Error
		cloned.Error = &errorCopy
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
			if len(cloned.Content[i].Details) > 0 {
				cloned.Content[i].Details = append([]byte(nil), cloned.Content[i].Details...)
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
			if len(cloned.ToolCalls[i].Details) > 0 {
				cloned.ToolCalls[i].Details = append([]byte(nil), cloned.ToolCalls[i].Details...)
			}
		}
	}
	if msg.ToolResult != nil {
		cloned.ToolResult = &ai.ToolResult{
			ToolCallID: msg.ToolResult.ToolCallID,
			ToolName:   msg.ToolResult.ToolName,
			IsError:    msg.ToolResult.IsError,
			Details:    append([]byte(nil), msg.ToolResult.Details...),
			Usage:      msg.ToolResult.Usage,
			Metadata:   append([]byte(nil), msg.ToolResult.Metadata...),
			Logs:       append([]ai.LogEntry(nil), msg.ToolResult.Logs...),
		}
		for i := range cloned.ToolResult.Logs {
			cloned.ToolResult.Logs[i].Details = append([]byte(nil), cloned.ToolResult.Logs[i].Details...)
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
				if len(cloned.ToolResult.Content[i].Details) > 0 {
					cloned.ToolResult.Content[i].Details = append([]byte(nil), cloned.ToolResult.Content[i].Details...)
				}
			}
		}
	}
	return cloned
}

func cloneMessages(messages []ai.Message) []ai.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]ai.Message, len(messages))
	for i, message := range messages {
		cloned[i] = cloneMessage(message)
	}
	return cloned
}
