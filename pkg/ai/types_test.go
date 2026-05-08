package ai

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestEventKinds(t *testing.T) {
	events := []struct {
		event Event
		want  EventKind
	}{
		{TextDelta{Text: "hello"}, EventTextDelta},
		{ToolCallEvent{ToolCall: ToolCall{ID: "call_1", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)}}, EventToolCall},
		{UsageEvent{Usage: Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}}, EventUsage},
		{StopEvent{Reason: StopReasonStop}, EventStop},
		{NewErrorEvent("provider_error", errors.New("boom")), EventError},
	}

	for _, tc := range events {
		if got := tc.event.Kind(); got != tc.want {
			t.Fatalf("%T Kind() = %q, want %q", tc.event, got, tc.want)
		}
	}
}

func TestErrorEventErrorMessage(t *testing.T) {
	e := ErrorEvent{Message: "something went wrong", Code: "code_1", Err: errors.New("inner")}
	if got := e.Error(); got != "something went wrong" {
		t.Fatalf("Error() = %q, want %q", got, "something went wrong")
	}
}

func TestErrorEventErrorFallbackToErr(t *testing.T) {
	e := ErrorEvent{Code: "code_1", Err: errors.New("inner error")}
	if got := e.Error(); got != "inner error" {
		t.Fatalf("Error() = %q, want %q", got, "inner error")
	}
}

func TestErrorEventErrorFallbackToCode(t *testing.T) {
	e := ErrorEvent{Code: "rate_limit"}
	if got := e.Error(); got != "rate_limit" {
		t.Fatalf("Error() = %q, want %q", got, "rate_limit")
	}
}

func TestErrorEventErrorAllEmpty(t *testing.T) {
	e := ErrorEvent{}
	if got := e.Error(); got != "provider stream error" {
		t.Fatalf("Error() = %q, want %q", got, "provider stream error")
	}
}

func TestErrorEventUnwrap(t *testing.T) {
	inner := errors.New("wrapped")
	e := ErrorEvent{Err: inner}
	if got := e.Unwrap(); got != inner {
		t.Fatalf("Unwrap() = %v, want %v", got, inner)
	}
}

func TestErrorEventUnwrapNil(t *testing.T) {
	e := ErrorEvent{}
	if got := e.Unwrap(); got != nil {
		t.Fatalf("Unwrap() = %v, want nil", got)
	}
}

func TestNewErrorEventWithError(t *testing.T) {
	inner := errors.New("boom")
	e := NewErrorEvent("provider_error", inner)
	if e.Code != "provider_error" {
		t.Fatalf("Code = %q, want %q", e.Code, "provider_error")
	}
	if e.Message != "boom" {
		t.Fatalf("Message = %q, want %q", e.Message, "boom")
	}
	if e.Err != inner {
		t.Fatal("Err not identical to input")
	}
}

func TestNewErrorEventWithNil(t *testing.T) {
	e := NewErrorEvent("generic", nil)
	if e.Code != "generic" {
		t.Fatalf("Code = %q, want %q", e.Code, "generic")
	}
	if e.Message != "provider stream error" {
		t.Fatalf("Message = %q, want %q", e.Message, "provider stream error")
	}
	if e.Err == nil {
		t.Fatal("Err = nil, want non-nil generic error")
	}
}

func TestMessageJSONRoundTrip(t *testing.T) {
	original := Message{
		Role:    RoleAssistant,
		Content: []ContentBlock{{Type: ContentText, Text: "hello"}},
		ToolCalls: []ToolCall{
			{ID: "call_1", Name: "read_file", Arguments: json.RawMessage(`{"path":"x"}`)},
		},
		Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Role != original.Role {
		t.Fatalf("Role = %q, want %q", decoded.Role, original.Role)
	}
	if len(decoded.Content) != 1 || decoded.Content[0].Text != "hello" {
		t.Fatalf("Content mismatch: %+v", decoded.Content)
	}
	if len(decoded.ToolCalls) != 1 || decoded.ToolCalls[0].ID != "call_1" {
		t.Fatalf("ToolCalls mismatch: %+v", decoded.ToolCalls)
	}
}

func TestContentBlockJSONRoundTrip(t *testing.T) {
	original := ContentBlock{
		Type:      ContentThinking,
		Thinking:  "step 1",
		Signature: "sig-abc",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded ContentBlock
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Type != ContentThinking {
		t.Fatalf("Type = %q, want %q", decoded.Type, ContentThinking)
	}
	if decoded.Thinking != "step 1" {
		t.Fatalf("Thinking = %q, want %q", decoded.Thinking, "step 1")
	}
	if decoded.Signature != "sig-abc" {
		t.Fatalf("Signature = %q, want %q", decoded.Signature, "sig-abc")
	}
}

func TestToolCallJSONRoundTrip(t *testing.T) {
	original := ToolCall{
		ID:        "tc-1",
		Name:      "write_file",
		Arguments: json.RawMessage(`{"path":"/tmp/x","content":"y"}`),
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded ToolCall
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.ID != original.ID {
		t.Fatalf("ID = %q, want %q", decoded.ID, original.ID)
	}
	if decoded.Name != original.Name {
		t.Fatalf("Name = %q, want %q", decoded.Name, original.Name)
	}
	if string(decoded.Arguments) != string(original.Arguments) {
		t.Fatalf("Arguments = %s, want %s", decoded.Arguments, original.Arguments)
	}
}

func TestToolResultJSONRoundTrip(t *testing.T) {
	original := ToolResult{
		ToolCallID: "tc-1",
		ToolName:   "read_file",
		Content:    []ContentBlock{{Type: ContentText, Text: "file contents"}},
		IsError:    true,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded ToolResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.ToolCallID != original.ToolCallID {
		t.Fatalf("ToolCallID = %q, want %q", decoded.ToolCallID, original.ToolCallID)
	}
	if !decoded.IsError {
		t.Fatal("IsError = false, want true")
	}
	if len(decoded.Content) != 1 || decoded.Content[0].Text != "file contents" {
		t.Fatalf("Content mismatch: %+v", decoded.Content)
	}
}

func TestConstantValues(t *testing.T) {
	tests := []struct {
		got  string
		want string
	}{
		{string(TransportAuto), "auto"},
		{string(TransportSSE), "sse"},
		{string(TransportWebSocket), "websocket"},
		{string(CacheRetentionNone), "none"},
		{string(CacheRetentionShort), "short"},
		{string(CacheRetentionLong), "long"},
		{string(ThinkingMinimal), "minimal"},
		{string(ThinkingLow), "low"},
		{string(ThinkingMedium), "medium"},
		{string(ThinkingHigh), "high"},
		{string(ThinkingXHigh), "xhigh"},
		{string(InputText), "text"},
		{string(InputImage), "image"},
		{string(RoleUser), "user"},
		{string(RoleAssistant), "assistant"},
		{string(RoleToolResult), "tool_result"},
		{string(ContentText), "text"},
		{string(ContentThinking), "thinking"},
		{string(ContentImage), "image"},
		{string(StopReasonStop), "stop"},
		{string(StopReasonLength), "length"},
		{string(StopReasonToolUse), "tool_use"},
		{string(StopReasonError), "error"},
		{string(StopReasonAborted), "aborted"},
		{string(EventTextDelta), "text_delta"},
		{string(EventToolCall), "tool_call"},
		{string(EventUsage), "usage"},
		{string(EventStop), "stop"},
		{string(EventError), "error"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Fatalf("constant = %q, want %q", tc.got, tc.want)
		}
	}
}

func TestModelJSONRoundTrip(t *testing.T) {
	original := Model{
		ID:            "gpt-4",
		Name:          "GPT-4",
		Provider:      "openai",
		Reasoning:     true,
		Input:         []InputKind{InputText, InputImage},
		Cost:          Cost{Input: 5.0, Output: 15.0},
		ContextWindow: 128000,
		MaxTokens:     4096,
		Headers:       map[string]string{"x-custom": "value"},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Model
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.ID != original.ID {
		t.Fatalf("ID = %q, want %q", decoded.ID, original.ID)
	}
	if decoded.ContextWindow != original.ContextWindow {
		t.Fatalf("ContextWindow = %d, want %d", decoded.ContextWindow, original.ContextWindow)
	}
	if decoded.Cost.Input != original.Cost.Input {
		t.Fatalf("Cost.Input = %f, want %f", decoded.Cost.Input, original.Cost.Input)
	}
	if len(decoded.Input) != 2 {
		t.Fatalf("len(Input) = %d, want 2", len(decoded.Input))
	}
}

func TestContextJSONRoundTrip(t *testing.T) {
	original := Context{
		SystemPrompt: "You are helpful",
		Messages: []Message{
			{Role: RoleUser, Content: []ContentBlock{{Type: ContentText, Text: "hi"}}},
		},
		Tools: []Tool{{Name: "calc", Description: "calculator"}},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Context
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.SystemPrompt != original.SystemPrompt {
		t.Fatalf("SystemPrompt = %q, want %q", decoded.SystemPrompt, original.SystemPrompt)
	}
	if len(decoded.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(decoded.Messages))
	}
	if len(decoded.Tools) != 1 || decoded.Tools[0].Name != "calc" {
		t.Fatalf("Tools mismatch: %+v", decoded.Tools)
	}
}

func TestUsageJSONRoundTrip(t *testing.T) {
	original := Usage{
		InputTokens:      10,
		OutputTokens:     20,
		CacheReadTokens:  5,
		CacheWriteTokens: 3,
		TotalTokens:      30,
		Cost:             UsageCost{Input: 0.5, Output: 1.5, Total: 2.0},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Usage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.InputTokens != original.InputTokens {
		t.Fatalf("InputTokens = %d, want %d", decoded.InputTokens, original.InputTokens)
	}
	if decoded.Cost.Total != original.Cost.Total {
		t.Fatalf("Cost.Total = %f, want %f", decoded.Cost.Total, original.Cost.Total)
	}
}

func TestToolJSONRoundTrip(t *testing.T) {
	original := Tool{
		Name:        "read_file",
		Description: "Reads a file",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Tool
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Name != original.Name {
		t.Fatalf("Name = %q, want %q", decoded.Name, original.Name)
	}
	if string(decoded.InputSchema) != string(original.InputSchema) {
		t.Fatalf("InputSchema mismatch")
	}
}

func TestContentBlockImageRoundTrip(t *testing.T) {
	original := ContentBlock{
		Type:          ContentImage,
		ImageData:     []byte{0x89, 0x50, 0x4E, 0x47},
		ImageMIMEType: "image/png",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded ContentBlock
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Type != ContentImage {
		t.Fatalf("Type = %q, want %q", decoded.Type, ContentImage)
	}
	if string(decoded.ImageData) != string(original.ImageData) {
		t.Fatalf("ImageData mismatch")
	}
	if decoded.ImageMIMEType != "image/png" {
		t.Fatalf("ImageMIMEType = %q, want %q", decoded.ImageMIMEType, "image/png")
	}
}
