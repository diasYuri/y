package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// HTTPAdapter exposes the versioned command protocol over HTTP JSON and
// exposes durable events as Server-Sent Events. It is deliberately thin: all
// authorization, idempotency and execution remain in the command handler.
//
// POST /v1/commands accepts a Command and returns a Response.
// GET  /v1/events?run_id=...&after=... returns events already persisted after
// the supplied sequence. Callers can poll or reconnect with the last id.
type HTTPAdapter struct {
	Handler      Handler
	Events       EventStore
	MaxBodyBytes int64
}

// ServeHTTP implements http.Handler.
func (a HTTPAdapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/commands":
		a.serveCommand(w, r)
	case "/v1/events":
		a.serveEvents(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a HTTPAdapter) serveCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.Handler == nil {
		http.Error(w, "runtime command handler is unavailable", http.StatusServiceUnavailable)
		return
	}
	limit := a.MaxBodyBytes
	if limit <= 0 {
		limit = 8 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	defer func() { _ = r.Body.Close() }()
	var command Command
	if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
		writeHTTPResponse(w, http.StatusBadRequest, Response{SchemaVersion: ProtocolVersion, Accepted: false, Error: &CommandError{Code: "invalid_command", Message: err.Error()}})
		return
	}
	response, err := dispatch(r.Context(), a.Handler, command)
	if err != nil && response.Error == nil {
		response = Response{SchemaVersion: ProtocolVersion, CommandID: command.ID, RunID: command.RunID, Accepted: false, Error: &CommandError{Code: "dispatch_failed", Message: err.Error()}}
	}
	status := http.StatusOK
	if !response.Accepted {
		status = http.StatusBadRequest
	}
	writeHTTPResponse(w, status, response)
}

func (a HTTPAdapter) serveEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.Events == nil {
		http.Error(w, "runtime event store is unavailable", http.StatusServiceUnavailable)
		return
	}
	runID := r.URL.Query().Get("run_id")
	if runID == "" {
		http.Error(w, "run_id is required", http.StatusBadRequest)
		return
	}
	after, err := sequenceAfter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	events, err := a.Events.Read(r.Context(), runID, after)
	if err != nil {
		if errors.Is(err, ErrStateNotFound) {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("read events: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			http.Error(w, fmt.Sprintf("encode event: %v", err), http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, payload)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func sequenceAfter(r *http.Request) (uint64, error) {
	raw := r.URL.Query().Get("after")
	if raw == "" {
		raw = r.Header.Get("Last-Event-ID")
	}
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid event sequence %q", raw)
	}
	return value, nil
}

func writeHTTPResponse(w http.ResponseWriter, status int, response Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}
