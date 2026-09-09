package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPAdapterDispatchesCommand(t *testing.T) {
	command, err := NewCommand(CommandAbort, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	command.ID, command.RunID = "command-1", "run-1"
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	adapter := HTTPAdapter{Handler: HandlerFunc(func(_ context.Context, got Command) (Response, error) {
		if got.ID != "command-1" {
			t.Fatalf("command = %#v", got)
		}
		return Response{Accepted: true}, nil
	})}
	request := httptest.NewRequest(http.MethodPost, "/v1/commands", bytes.NewReader(raw))
	recorder := httptest.NewRecorder()
	adapter.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Accepted || response.CommandID != command.ID || response.RunID != command.RunID {
		t.Fatalf("response = %#v", response)
	}
}

func TestHTTPAdapterStreamsStoredEventsAsSSE(t *testing.T) {
	store := NewInMemoryEventStore()
	event, err := NewEventEnvelope(EventAgentStarted, "run-1", 1, map[string]string{"state": "idle"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), "run-1", []EventEnvelope{event}); err != nil {
		t.Fatal(err)
	}
	adapter := HTTPAdapter{Events: store}
	request := httptest.NewRequest(http.MethodGet, "/v1/events?run_id=run-1&after=0", nil)
	recorder := httptest.NewRecorder()
	adapter.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "id: 1") || !strings.Contains(recorder.Body.String(), "event: agent_started") {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
}
