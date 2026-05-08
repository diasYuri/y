package compaction

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

func TestEstimateTokensEmptyMessage(t *testing.T) {
	msg := ai.Message{Role: ai.RoleUser}
	if got := EstimateTokens(msg); got != 0 {
		t.Fatalf("EstimateTokens(empty) = %d, want 0", got)
	}
}

func TestEstimateTokensASCIIText(t *testing.T) {
	// 40 ASCII chars -> 40/4 = 10 tokens
	msg := ai.Message{
		Role:    ai.RoleUser,
		Content: []ai.ContentBlock{{Type: ai.ContentText, Text: strings.Repeat("a", 40)}},
	}
	if got := EstimateTokens(msg); got != 10 {
		t.Fatalf("EstimateTokens(40 ascii chars) = %d, want 10", got)
	}
}

func TestEstimateTokensPartialToken(t *testing.T) {
	// 41 ASCII chars -> 41/4 = 10 remainder 1 -> 11 tokens
	msg := ai.Message{
		Role:    ai.RoleUser,
		Content: []ai.ContentBlock{{Type: ai.ContentText, Text: strings.Repeat("b", 41)}},
	}
	if got := EstimateTokens(msg); got != 11 {
		t.Fatalf("EstimateTokens(41 ascii chars) = %d, want 11", got)
	}
}

func TestEstimateTokensNonASCII(t *testing.T) {
	// Each non-ASCII rune counts as 1 token.
	msg := ai.Message{
		Role:    ai.RoleUser,
		Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "こんにちは"}}, // 5 runes
	}
	if got := EstimateTokens(msg); got != 5 {
		t.Fatalf("EstimateTokens(5 non-ascii runes) = %d, want 5", got)
	}
}

func TestEstimateTokensMixed(t *testing.T) {
	// 4 ASCII chars (1 token) + 2 non-ASCII runes (2 tokens) = 3 tokens
	msg := ai.Message{
		Role:    ai.RoleUser,
		Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "abcd日本"}},
	}
	if got := EstimateTokens(msg); got != 3 {
		t.Fatalf("EstimateTokens(mixed) = %d, want 3", got)
	}
}

func TestEstimateTranscriptTokens(t *testing.T) {
	messages := []ai.Message{
		{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: strings.Repeat("x", 40)}}},
		{Role: ai.RoleAssistant, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: strings.Repeat("y", 40)}}},
	}
	if got := EstimateTranscriptTokens(messages); got != 20 {
		t.Fatalf("EstimateTranscriptTokens = %d, want 20", got)
	}
}

func TestProviderAdjustment(t *testing.T) {
	tests := []struct {
		provider string
		want     float64
	}{
		{"openai", 1.0},
		{"anthropic", 0.95},
		{"google", 1.05},
		{"unknown", 1.0},
		{"OpenAI", 1.0}, // case insensitive
	}
	for _, tt := range tests {
		if got := ProviderAdjustment(tt.provider); got != tt.want {
			t.Fatalf("ProviderAdjustment(%q) = %f, want %f", tt.provider, got, tt.want)
		}
	}
}

func TestAdjustedEstimate(t *testing.T) {
	// 100 tokens * 0.95 (anthropic) = 95
	if got := AdjustedEstimate(100, "anthropic"); got != 95 {
		t.Fatalf("AdjustedEstimate = %d, want 95", got)
	}
}

func TestCompactorMaybeCompactDoesNotTriggerBelowThreshold(t *testing.T) {
	compactor := NewCompactor()
	transcript := []ai.Message{
		{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "hello"}}},
	}
	model := ai.Model{ID: "test", ContextWindow: 100000}

	result, compacted, err := compactor.MaybeCompact(context.Background(), transcript, nil, model)
	if err != nil {
		t.Fatalf("MaybeCompact returned error: %v", err)
	}
	if compacted {
		t.Fatal("compacted = true, want false")
	}
	if len(result) != len(transcript) {
		t.Fatalf("result length = %d, want %d", len(result), len(transcript))
	}
}

func TestCompactorMaybeCompactTriggersAboveThreshold(t *testing.T) {
	// Build a transcript that exceeds 80% of a 1000-token context window.
	// Each message with 400 ASCII chars = 100 tokens.  9 messages = 900 tokens > 800 threshold.
	var transcript []ai.Message
	for i := 0; i < 9; i++ {
		transcript = append(transcript, ai.Message{
			Role:      ai.RoleUser,
			Timestamp: time.Now().UTC(),
			Content:   []ai.ContentBlock{{Type: ai.ContentText, Text: strings.Repeat("a", 400)}},
		})
	}

	provider := &fakeSummarizer{summary: "summary text"}
	compactor := NewCompactor()
	model := ai.Model{ID: "test", ContextWindow: 1000}

	result, compacted, err := compactor.MaybeCompact(context.Background(), transcript, provider, model)
	if err != nil {
		t.Fatalf("MaybeCompact returned error: %v", err)
	}
	if !compacted {
		t.Fatal("compacted = false, want true")
	}

	// Result should have: first message + summary + last 6 messages = 8 messages.
	// But since 9 - 6 = 3, and we keep first message at index 0, the cutoff is max(1, 3) = 3.
	// So result = [msg0, summary, msg3, msg4, msg5, msg6, msg7, msg8] = 8 messages.
	if len(result) != 8 {
		t.Fatalf("result length = %d, want 8", len(result))
	}

	// Verify the summary is present.
	foundSummary := false
	for _, msg := range result {
		if msg.Role == ai.RoleUser && strings.Contains(contentText(msg.Content), "[Session summary]") {
			foundSummary = true
			if !strings.Contains(contentText(msg.Content), "summary text") {
				t.Fatal("summary text not found in summary message")
			}
		}
	}
	if !foundSummary {
		t.Fatal("summary message not found in result")
	}
}

func TestCompactorMaybeCompactNilProvider(t *testing.T) {
	var transcript []ai.Message
	for i := 0; i < 9; i++ {
		transcript = append(transcript, ai.Message{
			Role:    ai.RoleUser,
			Content: []ai.ContentBlock{{Type: ai.ContentText, Text: strings.Repeat("a", 400)}},
		})
	}

	compactor := NewCompactor()
	model := ai.Model{ID: "test", ContextWindow: 1000}

	_, _, err := compactor.MaybeCompact(context.Background(), transcript, nil, model)
	if err == nil {
		t.Fatal("expected error for nil provider")
	}
}

func TestCompactorMaybeCompactZeroContextWindow(t *testing.T) {
	compactor := NewCompactor()
	transcript := []ai.Message{
		{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: strings.Repeat("x", 4000)}}},
	}
	model := ai.Model{ID: "test", ContextWindow: 0}

	result, compacted, err := compactor.MaybeCompact(context.Background(), transcript, nil, model)
	if err != nil {
		t.Fatalf("MaybeCompact returned error: %v", err)
	}
	if compacted {
		t.Fatal("compacted = true, want false when ContextWindow is 0")
	}
	if len(result) != 1 {
		t.Fatalf("result length = %d, want 1", len(result))
	}
}

func TestCompactorRewriteKeepsLastN(t *testing.T) {
	compactor := &Compactor{Threshold: 0.8, KeepLast: 2}
	transcript := []ai.Message{
		{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "msg1"}}},
		{Role: ai.RoleAssistant, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "msg2"}}},
		{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "msg3"}}},
		{Role: ai.RoleAssistant, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "msg4"}}},
		{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "msg5"}}},
	}

	rewritten := compactor.rewrite(transcript, "summary", 2)
	// Result: [msg1, summary, msg4, msg5] = 4 messages
	if len(rewritten) != 4 {
		t.Fatalf("rewritten length = %d, want 4", len(rewritten))
	}
	if contentText(rewritten[0].Content) != "msg1" {
		t.Fatalf("first message = %q, want msg1", contentText(rewritten[0].Content))
	}
	if !strings.Contains(contentText(rewritten[1].Content), "summary") {
		t.Fatalf("second message = %q, want summary", contentText(rewritten[1].Content))
	}
	if contentText(rewritten[2].Content) != "msg4" {
		t.Fatalf("third message = %q, want msg4", contentText(rewritten[2].Content))
	}
	if contentText(rewritten[3].Content) != "msg5" {
		t.Fatalf("fourth message = %q, want msg5", contentText(rewritten[3].Content))
	}
}

func TestCompactorRewriteNotEnoughMessages(t *testing.T) {
	compactor := &Compactor{Threshold: 0.8, KeepLast: 10}
	transcript := []ai.Message{
		{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "msg1"}}},
		{Role: ai.RoleAssistant, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "msg2"}}},
	}

	// KeepLast >= len(transcript), so rewrite returns original unchanged.
	rewritten := compactor.rewrite(transcript, "summary", 10)
	if len(rewritten) != 2 {
		t.Fatalf("rewritten length = %d, want 2", len(rewritten))
	}
	if contentText(rewritten[0].Content) != "msg1" {
		t.Fatalf("first message = %q, want msg1", contentText(rewritten[0].Content))
	}
	if contentText(rewritten[1].Content) != "msg2" {
		t.Fatalf("second message = %q, want msg2", contentText(rewritten[1].Content))
	}
}

func TestCompactorSummarizeHandlesStreamError(t *testing.T) {
	provider := &fakeSummarizer{err: errors.New("stream failed")}
	compactor := NewCompactor()
	var transcript []ai.Message
	for i := 0; i < 9; i++ {
		transcript = append(transcript, ai.Message{
			Role:    ai.RoleUser,
			Content: []ai.ContentBlock{{Type: ai.ContentText, Text: strings.Repeat("a", 400)}},
		})
	}
	model := ai.Model{ID: "test", ContextWindow: 1000}

	_, _, err := compactor.MaybeCompact(context.Background(), transcript, provider, model)
	if err == nil {
		t.Fatal("expected error from stream failure")
	}
}

func TestCompactorSummarizeHandlesErrorEvent(t *testing.T) {
	provider := &fakeSummarizer{events: []ai.Event{
		ai.ErrorEvent{Code: "test", Message: "something went wrong"},
	}}
	compactor := NewCompactor()
	var transcript []ai.Message
	for i := 0; i < 9; i++ {
		transcript = append(transcript, ai.Message{
			Role:    ai.RoleUser,
			Content: []ai.ContentBlock{{Type: ai.ContentText, Text: strings.Repeat("a", 400)}},
		})
	}
	model := ai.Model{ID: "test", ContextWindow: 1000}

	_, _, err := compactor.MaybeCompact(context.Background(), transcript, provider, model)
	if err == nil {
		t.Fatal("expected error from error event")
	}
}

// fakeSummarizer is a test double that returns a canned summary.
type fakeSummarizer struct {
	summary string
	err     error
	events  []ai.Event
}

func (f *fakeSummarizer) Stream(ctx context.Context, req providers.StreamRequest) (providers.EventStream, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(f.events) > 0 {
		return &fakeEventStream{events: f.events}, nil
	}
	return &fakeEventStream{
		events: []ai.Event{
			ai.TextDelta{Text: f.summary},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}, nil
}

type fakeEventStream struct {
	events []ai.Event
	index  int
	closed bool
}

func (s *fakeEventStream) Next(ctx context.Context) (ai.Event, error) {
	if s.closed {
		return nil, errors.New("stream closed")
	}
	if s.index < len(s.events) {
		event := s.events[s.index]
		s.index++
		return event, nil
	}
	return nil, io.EOF
}

func (s *fakeEventStream) Close() error {
	s.closed = true
	return nil
}
