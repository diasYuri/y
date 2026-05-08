//go:build feature_telemetry

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// DefaultEmitter is the active OTLP emitter when telemetry is compiled in.
var DefaultEmitter Emitter = NewOTLPEmitter("")

// OTLPEmitter sends telemetry events as JSON POST requests to a configured
// OTLP/HTTP endpoint.
type OTLPEmitter struct {
	endpoint string
	client   *http.Client
	mu       sync.Mutex
	buffer   []Event
}

// NewOTLPEmitter creates an OTLP emitter that sends to the given endpoint.
// If endpoint is empty, the Y_TELEMETRY_ENDPOINT environment variable is
// consulted.  If still empty, events are buffered but never sent.
func NewOTLPEmitter(endpoint string) *OTLPEmitter {
	if endpoint == "" {
		endpoint = os.Getenv("Y_TELEMETRY_ENDPOINT")
	}
	return &OTLPEmitter{
		endpoint: endpoint,
		client:   &http.Client{Timeout: 10 * time.Second},
		buffer:   make([]Event, 0, 64),
	}
}

// Emit buffers the event.  When the buffer reaches 64 events, a flush is
// triggered in a background goroutine.
func (e *OTLPEmitter) Emit(event Event) {
	e.mu.Lock()
	e.buffer = append(e.buffer, event)
	shouldFlush := len(e.buffer) >= 64
	e.mu.Unlock()
	if shouldFlush {
		go e.Flush()
	}
}

// Flush sends all buffered events to the configured endpoint.
func (e *OTLPEmitter) Flush() error {
	if e.endpoint == "" {
		return nil
	}

	e.mu.Lock()
	if len(e.buffer) == 0 {
		e.mu.Unlock()
		return nil
	}
	batch := e.buffer
	e.buffer = make([]Event, 0, 64)
	e.mu.Unlock()

	payload, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("marshal telemetry batch: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create telemetry request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("send telemetry batch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("telemetry endpoint returned %d", resp.StatusCode)
	}
	return nil
}
