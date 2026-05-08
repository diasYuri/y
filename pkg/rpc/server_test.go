//go:build feature_rpc

package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

// fakeProvider implements providers.Provider for tests.
type fakeProvider struct {
	models []ai.Model
}

func (f *fakeProvider) ID() string { return "fake" }

func (f *fakeProvider) Models(ctx context.Context) ([]ai.Model, error) {
	return f.models, nil
}

func (f *fakeProvider) Stream(ctx context.Context, req providers.StreamRequest) (providers.EventStream, error) {
	return &fakeEventStream{
		events: []ai.Event{
			ai.TextDelta{ContentIndex: 0, Text: "hello from fake"},
			ai.StopEvent{Reason: ai.StopReasonStop},
		},
	}, nil
}

// fakeEventStream implements providers.EventStream for tests.
type fakeEventStream struct {
	events []ai.Event
	idx    int
	closed bool
}

func (s *fakeEventStream) Next(ctx context.Context) (ai.Event, error) {
	if s.closed {
		return nil, providers.ErrStreamClosed
	}
	if s.idx >= len(s.events) {
		return nil, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *fakeEventStream) Close() error {
	s.closed = true
	return nil
}

func newTestServer() *Server {
	cfg := ServerConfig{
		Addr: ":0",
		Log:  io.Discard,
		Provider: &fakeProvider{
			models: []ai.Model{
				{ID: "gpt-test", Name: "Test Model", Provider: "fake", Input: []ai.InputKind{ai.InputText}},
			},
		},
		ToolRegistry: nil,
	}
	return NewServer(cfg)
}

func rpcPost(s *Server, req Request) (*http.Response, Response) {
	body, _ := json.Marshal(req)
	httReq := httptest.NewRequest(http.MethodPost, "/rpc", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleRPC(rec, httReq)

	var resp Response
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Result(), resp
}

func TestServerHealth(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("health status = %q, want ok", body["status"])
	}
}

func TestServerRPCModels(t *testing.T) {
	s := newTestServer()
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "models"})
	if resp.Error != nil {
		t.Fatalf("models error: %v", resp.Error)
	}
	var models []modelInfo
	b, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(b, &models); err != nil {
		t.Fatalf("unmarshal models: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if models[0].ID != "gpt-test" {
		t.Fatalf("model id = %q, want gpt-test", models[0].ID)
	}
}

func TestServerRPCChat(t *testing.T) {
	s := newTestServer()
	params, _ := json.Marshal(chatParams{Message: "hello"})
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "chat", Params: params})
	if resp.Error != nil {
		t.Fatalf("chat error: %v", resp.Error)
	}
	var result chatResult
	b, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal chat result: %v", err)
	}
	if result.SessionID == "" {
		t.Fatal("expected session id")
	}
	if result.Response != "hello from fake" {
		t.Fatalf("response = %q, want hello from fake", result.Response)
	}
}

func TestServerRPCChatRequiresMessage(t *testing.T) {
	s := newTestServer()
	params, _ := json.Marshal(chatParams{Message: "   "})
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`3`), Method: "chat", Params: params})
	if resp.Error == nil {
		t.Fatal("expected error for empty message")
	}
	if resp.Error.Code != ErrInvalidParams {
		t.Fatalf("error code = %d, want %d", resp.Error.Code, ErrInvalidParams)
	}
}

func TestServerRPCTranscript(t *testing.T) {
	s := newTestServer()
	// First chat to create transcript with explicit session.
	chatParamsJSON, _ := json.Marshal(chatParams{Message: "hello world", SessionID: "test-sess"})
	rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`4`), Method: "chat", Params: chatParamsJSON})

	// Get transcript for same session.
	trParams, _ := json.Marshal(transcriptParams{SessionID: "test-sess"})
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`5`), Method: "transcript", Params: trParams})
	if resp.Error != nil {
		t.Fatalf("transcript error: %v", resp.Error)
	}
	var result transcriptResult
	b, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal transcript: %v", err)
	}
	if result.MessageCount != 2 {
		t.Fatalf("message count = %d, want 2", result.MessageCount)
	}
}

func TestServerRPCClear(t *testing.T) {
	s := newTestServer()
	// Chat then clear with explicit session.
	chatParamsJSON, _ := json.Marshal(chatParams{Message: "test", SessionID: "clear-sess"})
	rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`6`), Method: "chat", Params: chatParamsJSON})

	clearParams, _ := json.Marshal(clearParams{SessionID: "clear-sess"})
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`7`), Method: "clear", Params: clearParams})
	if resp.Error != nil {
		t.Fatalf("clear error: %v", resp.Error)
	}

	// Verify transcript is empty.
	trParams, _ := json.Marshal(transcriptParams{SessionID: "clear-sess"})
	_, resp = rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`8`), Method: "transcript", Params: trParams})
	var result transcriptResult
	b, _ := json.Marshal(resp.Result)
	_ = json.Unmarshal(b, &result)
	if result.MessageCount != 0 {
		t.Fatalf("message count after clear = %d, want 0", result.MessageCount)
	}
}

func TestServerRPCMethodNotFound(t *testing.T) {
	s := newTestServer()
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`9`), Method: "nonexistent"})
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != ErrMethodNotFound {
		t.Fatalf("error code = %d, want %d", resp.Error.Code, ErrMethodNotFound)
	}
}

func TestServerRPCNotification(t *testing.T) {
	s := newTestServer()
	params, _ := json.Marshal(chatParams{Message: "hello"})
	body, _ := json.Marshal(Request{JSONRPC: "2.0", Method: "chat", Params: params})
	req := httptest.NewRequest(http.MethodPost, "/rpc", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleRPC(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestServerRPCInvalidJSON(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/rpc", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	s.handleRPC(rec, req)

	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != ErrParseError {
		t.Fatalf("expected parse error, got %+v", resp.Error)
	}
}

func TestServerRPCHandlesNoTools(t *testing.T) {
	s := newTestServer()
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`10`), Method: "tools"})
	if resp.Error != nil {
		t.Fatalf("tools error: %v", resp.Error)
	}
	var tools []toolInfo
	b, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(b, &tools); err != nil {
		t.Fatalf("unmarshal tools: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("len(tools) = %d, want 0", len(tools))
	}
}

func TestServerRPCHandlesGetMethod(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/rpc", nil)
	rec := httptest.NewRecorder()
	s.handleRPC(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestServerRPCChatWithSession(t *testing.T) {
	s := newTestServer()
	// Chat with explicit session ID.
	params, _ := json.Marshal(chatParams{Message: "first", SessionID: "my-session"})
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`11`), Method: "chat", Params: params})
	if resp.Error != nil {
		t.Fatalf("chat error: %v", resp.Error)
	}
	var result chatResult
	b, _ := json.Marshal(resp.Result)
	_ = json.Unmarshal(b, &result)
	if result.SessionID != "my-session" {
		t.Fatalf("session id = %q, want my-session", result.SessionID)
	}

	// Second message in same session.
	params2, _ := json.Marshal(chatParams{Message: "second", SessionID: "my-session"})
	_, resp2 := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`12`), Method: "chat", Params: params2})
	var result2 chatResult
	b2, _ := json.Marshal(resp2.Result)
	_ = json.Unmarshal(b2, &result2)

	// Transcript should have 4 messages (2 user + 2 assistant).
	_, tr := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`13`), Method: "transcript", Params: mustMarshal(chatParams{SessionID: "my-session"})})
	var trResult transcriptResult
	b3, _ := json.Marshal(tr.Result)
	_ = json.Unmarshal(b3, &trResult)
	if trResult.MessageCount != 4 {
		t.Fatalf("message count = %d, want 4", trResult.MessageCount)
	}
}

func TestServerRPCNoProvider(t *testing.T) {
	s := newTestServer()
	// Override provider to nil.
	s.cfg.Provider = nil
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`14`), Method: "models"})
	if resp.Error != nil {
		t.Fatalf("models error: %v", resp.Error)
	}
	var models []modelInfo
	b, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(b, &models); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("len(models) = %d, want 0", len(models))
	}
}

func TestEventBusSubscribeEmit(t *testing.T) {
	bus := newEventBus()
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	bus.Emit(newStreamEvent("test", "sess-1", "hello"))

	select {
	case ev := <-ch:
		if ev.Type != "test" {
			t.Fatalf("type = %q, want test", ev.Type)
		}
		if ev.SessionID != "sess-1" {
			t.Fatalf("session = %q, want sess-1", ev.SessionID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestEventBusMultipleSubscribers(t *testing.T) {
	bus := newEventBus()
	ch1 := bus.Subscribe()
	ch2 := bus.Subscribe()
	defer bus.Unsubscribe(ch1)
	defer bus.Unsubscribe(ch2)

	bus.Emit(newStreamEvent("test", "sess-1", "hello"))

	for _, ch := range []chan StreamEvent{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Type != "test" {
				t.Fatalf("type = %q, want test", ev.Type)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for event")
		}
	}
}

func TestEventBusUnsubscribe(t *testing.T) {
	bus := newEventBus()
	ch := bus.Subscribe()
	bus.Unsubscribe(ch)

	_, ok := <-ch
	if ok {
		t.Fatal("expected channel to be closed")
	}
}

func TestEventBusSlowSubscriberDrop(t *testing.T) {
	bus := newEventBus()
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	// Fill the channel buffer.
	for i := 0; i < 20; i++ {
		bus.Emit(newStreamEvent("test", "sess-1", i))
	}

	// Should not panic or block.
}

func TestServerEventsEndpoint(t *testing.T) {
	s := newTestServer()

	// Subscribe to events before triggering them.
	sub := s.events.Subscribe()
	defer s.events.Unsubscribe(sub)

	// Trigger a chat to generate events.
	params, _ := json.Marshal(chatParams{Message: "hello"})
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`20`), Method: "chat", Params: params})
	if resp.Error != nil {
		t.Fatalf("chat error: %v", resp.Error)
	}

	// Check that at least one event was emitted.
	var found bool
	timeout := time.After(2 * time.Second)
	for !found {
		select {
		case ev, ok := <-sub:
			if !ok {
				t.Fatal("event channel closed unexpectedly")
			}
			if ev.Type == "chat_completed" {
				found = true
			}
		case <-timeout:
			if !found {
				t.Fatal("timeout waiting for chat_completed event")
			}
		}
	}
}

func TestServerRPCContinue(t *testing.T) {
	s := newTestServer()
	// First chat to create transcript.
	chatParamsJSON, _ := json.Marshal(chatParams{Message: "hello", SessionID: "continue-sess"})
	rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`30`), Method: "chat", Params: chatParamsJSON})

	// Continue from the transcript.
	continueParamsJSON, _ := json.Marshal(continueParams{SessionID: "continue-sess"})
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`31`), Method: "continue", Params: continueParamsJSON})
	if resp.Error != nil {
		t.Fatalf("continue error: %v", resp.Error)
	}
	var result continueResult
	b, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal continue result: %v", err)
	}
	if result.SessionID != "continue-sess" {
		t.Fatalf("session id = %q, want continue-sess", result.SessionID)
	}
}

func TestServerRPCContinueRequiresTranscript(t *testing.T) {
	s := newTestServer()
	continueParamsJSON, _ := json.Marshal(continueParams{SessionID: "empty-sess"})
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`32`), Method: "continue", Params: continueParamsJSON})
	if resp.Error == nil {
		t.Fatal("expected error for empty transcript")
	}
	if resp.Error.Code != ErrInvalidRequest {
		t.Fatalf("error code = %d, want %d", resp.Error.Code, ErrInvalidRequest)
	}
}

func TestServerRPCSteer(t *testing.T) {
	s := newTestServer()
	steerParamsJSON, _ := json.Marshal(steerParams{Message: "please clarify", SessionID: "steer-sess"})
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`33`), Method: "steer", Params: steerParamsJSON})
	if resp.Error != nil {
		t.Fatalf("steer error: %v", resp.Error)
	}
	var result map[string]string
	b, _ := json.Marshal(resp.Result)
	_ = json.Unmarshal(b, &result)
	if result["status"] != "queued" {
		t.Fatalf("status = %q, want queued", result["status"])
	}

	// Verify transcript now has the steer message.
	_, tr := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`34`), Method: "transcript", Params: mustMarshal(transcriptParams{SessionID: "steer-sess"})})
	var trResult transcriptResult
	b2, _ := json.Marshal(tr.Result)
	_ = json.Unmarshal(b2, &trResult)
	if trResult.MessageCount != 1 {
		t.Fatalf("message count = %d, want 1", trResult.MessageCount)
	}
}

func TestServerRPCSteerRequiresMessage(t *testing.T) {
	s := newTestServer()
	steerParamsJSON, _ := json.Marshal(steerParams{Message: "   "})
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`35`), Method: "steer", Params: steerParamsJSON})
	if resp.Error == nil {
		t.Fatal("expected error for empty message")
	}
	if resp.Error.Code != ErrInvalidParams {
		t.Fatalf("error code = %d, want %d", resp.Error.Code, ErrInvalidParams)
	}
}

func TestServerRPCAbort(t *testing.T) {
	s := newTestServer()
	abortParamsJSON, _ := json.Marshal(abortParams{SessionID: "abort-sess"})
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`36`), Method: "abort", Params: abortParamsJSON})
	if resp.Error != nil {
		t.Fatalf("abort error: %v", resp.Error)
	}
	var result map[string]string
	b, _ := json.Marshal(resp.Result)
	_ = json.Unmarshal(b, &result)
	if result["status"] != "requested" {
		t.Fatalf("status = %q, want requested", result["status"])
	}
}

func TestServerRPCChatThenSteerThenContinue(t *testing.T) {
	s := newTestServer()
	// 1. Chat.
	chatParamsJSON, _ := json.Marshal(chatParams{Message: "hello", SessionID: "flow-sess"})
	rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`37`), Method: "chat", Params: chatParamsJSON})

	// 2. Steer.
	steerParamsJSON, _ := json.Marshal(steerParams{Message: "be more concise", SessionID: "flow-sess"})
	rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`38`), Method: "steer", Params: steerParamsJSON})

	// 3. Continue should pick up the steer message.
	continueParamsJSON, _ := json.Marshal(continueParams{SessionID: "flow-sess"})
	_, resp := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`39`), Method: "continue", Params: continueParamsJSON})
	if resp.Error != nil {
		t.Fatalf("continue error: %v", resp.Error)
	}

	// Transcript should have 4 messages: user hello + assistant response + steer message + assistant response.
	_, tr := rpcPost(s, Request{JSONRPC: "2.0", ID: json.RawMessage(`40`), Method: "transcript", Params: mustMarshal(transcriptParams{SessionID: "flow-sess"})})
	var trResult transcriptResult
	b, _ := json.Marshal(tr.Result)
	_ = json.Unmarshal(b, &trResult)
	if trResult.MessageCount != 4 {
		t.Fatalf("message count = %d, want 4", trResult.MessageCount)
	}
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
