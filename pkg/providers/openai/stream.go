package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

type streamResult struct {
	event ai.Event
	err   error
}

type stream struct {
	body     io.ReadCloser
	cancel   context.CancelFunc
	maxEvent int64
	results  chan streamResult
	done     chan struct{}
	once     sync.Once
}

func newStream(body io.ReadCloser, maxEvent int64, cancel context.CancelFunc) providers.EventStream {
	if maxEvent <= 0 {
		maxEvent = defaultMaxEvent
	}
	s := &stream{
		body:     body,
		cancel:   cancel,
		maxEvent: maxEvent,
		results:  make(chan streamResult, 4),
		done:     make(chan struct{}),
	}
	go s.readLoop()
	return s
}

func (s *stream) Next(ctx context.Context) (ai.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-s.done:
		return nil, providers.ErrStreamClosed
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, providers.ErrStreamClosed
	case result, ok := <-s.results:
		if !ok {
			return nil, io.EOF
		}
		if result.err != nil {
			return nil, result.err
		}
		return result.event, nil
	}
}

func (s *stream) Close() error {
	s.once.Do(func() {
		close(s.done)
		if s.cancel != nil {
			s.cancel()
		}
		_ = s.body.Close()
	})
	return nil
}

func (s *stream) readLoop() {
	defer close(s.results)
	defer s.body.Close()
	defer func() {
		if s.cancel != nil {
			s.cancel()
		}
	}()

	reader := bufio.NewReader(s.body)
	state := newResponseState()
	for {
		data, err := readSSEData(reader, s.maxEvent)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			s.emit(ai.NewErrorEvent("openai_stream_read", err))
			return
		}
		if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			continue
		}
		for _, event := range state.consume(data) {
			if !s.emit(event) {
				return
			}
		}
	}
}

func (s *stream) emit(event ai.Event) bool {
	select {
	case <-s.done:
		return false
	case s.results <- streamResult{event: event}:
		return true
	}
}

func readSSEData(r *bufio.Reader, maxEvent int64) ([]byte, error) {
	var data bytes.Buffer
	for {
		line, err := readLine(r, maxEvent)
		if err != nil {
			if errors.Is(err, io.EOF) && data.Len() > 0 {
				return data.Bytes(), nil
			}
			return nil, err
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			return data.Bytes(), nil
		}
		if bytes.HasPrefix(line, []byte(":")) {
			continue
		}
		name, value, ok := bytes.Cut(line, []byte(":"))
		if !ok || !bytes.Equal(name, []byte("data")) {
			continue
		}
		value = bytes.TrimPrefix(value, []byte(" "))
		if int64(data.Len()+len(value)+1) > maxEvent {
			return nil, fmt.Errorf("openai SSE event exceeds %d bytes", maxEvent)
		}
		if data.Len() > 0 {
			data.WriteByte('\n')
		}
		data.Write(value)
	}
}

func readLine(r *bufio.Reader, maxEvent int64) ([]byte, error) {
	var out bytes.Buffer
	for {
		part, err := r.ReadSlice('\n')
		if len(part) > 0 {
			if int64(out.Len()+len(part)) > maxEvent {
				return nil, fmt.Errorf("openai SSE line exceeds %d bytes", maxEvent)
			}
			out.Write(part)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return out.Bytes(), err
		}
		return out.Bytes(), nil
	}
}

type responseState struct {
	tools   map[string]*toolState
	toolUse bool
}

type toolState struct {
	id        string
	callID    string
	itemID    string
	name      string
	arguments strings.Builder
	complete  bool
}

func newResponseState() *responseState {
	return &responseState{tools: make(map[string]*toolState)}
}

func (s *responseState) consume(data []byte) []ai.Event {
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return []ai.Event{ai.NewErrorEvent("openai_stream_json", err)}
	}

	switch header.Type {
	case "response.output_text.delta", "response.refusal.delta":
		var event struct {
			Delta        string `json:"delta"`
			ContentIndex int    `json:"content_index"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return []ai.Event{ai.NewErrorEvent("openai_stream_json", err)}
		}
		if event.Delta == "" {
			return nil
		}
		return []ai.Event{ai.TextDelta{ContentIndex: event.ContentIndex, Text: event.Delta}}
	case "response.output_item.added":
		var event outputItemEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return []ai.Event{ai.NewErrorEvent("openai_stream_json", err)}
		}
		if event.Item.Type == "function_call" {
			s.rememberTool(event.Item)
			s.toolUse = true
		}
	case "response.function_call_arguments.delta":
		var event struct {
			ItemID      string `json:"item_id"`
			OutputIndex int    `json:"output_index"`
			Delta       string `json:"delta"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return []ai.Event{ai.NewErrorEvent("openai_stream_json", err)}
		}
		tool := s.toolFor(event.ItemID, event.OutputIndex)
		tool.arguments.WriteString(event.Delta)
		return []ai.Event{ai.ToolCallEvent{
			ContentIndex: event.OutputIndex,
			ToolCall: ai.ToolCall{
				ID:   tool.id,
				Name: tool.name,
			},
			ArgumentsDelta: json.RawMessage(event.Delta),
			Complete:       false,
		}}
	case "response.function_call_arguments.done":
		var event struct {
			ItemID      string `json:"item_id"`
			OutputIndex int    `json:"output_index"`
			Name        string `json:"name"`
			Arguments   string `json:"arguments"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return []ai.Event{ai.NewErrorEvent("openai_stream_json", err)}
		}
		tool := s.toolFor(event.ItemID, event.OutputIndex)
		if event.Name != "" {
			tool.name = event.Name
		}
		if event.Arguments != "" {
			tool.arguments.Reset()
			tool.arguments.WriteString(event.Arguments)
		}
		return []ai.Event{s.completeTool(event.OutputIndex, tool)}
	case "response.output_item.done":
		var event outputItemEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return []ai.Event{ai.NewErrorEvent("openai_stream_json", err)}
		}
		if event.Item.Type == "function_call" {
			tool := s.rememberTool(event.Item)
			if event.Item.Arguments != "" {
				tool.arguments.Reset()
				tool.arguments.WriteString(event.Item.Arguments)
			}
			if !tool.complete {
				return []ai.Event{s.completeTool(event.OutputIndex, tool)}
			}
		}
	case "response.completed":
		var event completedEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return []ai.Event{ai.NewErrorEvent("openai_stream_json", err)}
		}
		events := make([]ai.Event, 0, 2)
		if event.Response.Usage.TotalTokens != 0 || event.Response.Usage.InputTokens != 0 || event.Response.Usage.OutputTokens != 0 {
			cached := event.Response.Usage.InputTokenDetails.CachedTokens
			events = append(events, ai.UsageEvent{Usage: ai.Usage{
				InputTokens:      event.Response.Usage.InputTokens - cached,
				OutputTokens:     event.Response.Usage.OutputTokens,
				CacheReadTokens:  cached,
				CacheWriteTokens: 0,
				TotalTokens:      event.Response.Usage.TotalTokens,
			}})
		}
		reason := mapStatus(event.Response.Status)
		if s.toolUse && reason == ai.StopReasonStop {
			reason = ai.StopReasonToolUse
		}
		events = append(events, ai.StopEvent{Reason: reason})
		return events
	case "response.failed":
		var event failedEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return []ai.Event{ai.NewErrorEvent("openai_stream_json", err)}
		}
		return []ai.Event{
			ai.ErrorEvent{Code: event.Response.Error.Code, Message: event.Response.Error.Message},
			ai.StopEvent{Reason: ai.StopReasonError},
		}
	case "error":
		var event struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return []ai.Event{ai.NewErrorEvent("openai_stream_json", err)}
		}
		return []ai.Event{
			ai.ErrorEvent{Code: event.Code, Message: event.Message},
			ai.StopEvent{Reason: ai.StopReasonError},
		}
	}
	return nil
}

func (s *responseState) rememberTool(item outputItem) *toolState {
	key := item.ID
	if key == "" {
		key = item.CallID
	}
	if key == "" {
		key = fmt.Sprintf("%d", len(s.tools))
	}
	tool := s.tools[key]
	if tool == nil {
		tool = &toolState{itemID: item.ID, callID: item.CallID}
		s.tools[key] = tool
	}
	tool.itemID = item.ID
	tool.callID = item.CallID
	tool.name = item.Name
	tool.id = joinToolCallID(item.CallID, item.ID)
	if item.Arguments != "" && tool.arguments.Len() == 0 {
		tool.arguments.WriteString(item.Arguments)
	}
	return tool
}

func (s *responseState) toolFor(itemID string, outputIndex int) *toolState {
	if itemID != "" {
		if tool := s.tools[itemID]; tool != nil {
			return tool
		}
	}
	key := fmt.Sprintf("%d", outputIndex)
	if tool := s.tools[key]; tool != nil {
		return tool
	}
	tool := &toolState{itemID: itemID, id: joinToolCallID("", itemID)}
	s.tools[key] = tool
	if itemID != "" {
		s.tools[itemID] = tool
	}
	return tool
}

func (s *responseState) completeTool(contentIndex int, tool *toolState) ai.Event {
	tool.complete = true
	args := tool.arguments.String()
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	return ai.ToolCallEvent{
		ContentIndex: contentIndex,
		ToolCall: ai.ToolCall{
			ID:        tool.id,
			Name:      tool.name,
			Arguments: json.RawMessage(args),
		},
		Complete: true,
	}
}

func joinToolCallID(callID, itemID string) string {
	switch {
	case callID != "" && itemID != "":
		return callID + "|" + itemID
	case callID != "":
		return callID
	default:
		return itemID
	}
}

func mapStatus(status string) ai.StopReason {
	switch status {
	case "", "completed", "in_progress", "queued":
		return ai.StopReasonStop
	case "incomplete":
		return ai.StopReasonLength
	case "failed", "cancelled":
		return ai.StopReasonError
	default:
		return ai.StopReasonStop
	}
}

type outputItemEvent struct {
	OutputIndex int        `json:"output_index"`
	Item        outputItem `json:"item"`
}

type outputItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type completedEvent struct {
	Response struct {
		Status string `json:"status"`
		Usage  struct {
			InputTokens       int64 `json:"input_tokens"`
			OutputTokens      int64 `json:"output_tokens"`
			TotalTokens       int64 `json:"total_tokens"`
			InputTokenDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	} `json:"response"`
}

type failedEvent struct {
	Response struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
}
