package telemetry

import (
	"context"
	"sync"
	"time"
)

// Attribute is a typed, transport-neutral span attribute.
type Attribute struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

// Status describes the terminal outcome of a span.
type Status string

const (
	StatusUnset Status = "unset"
	StatusOK    Status = "ok"
	StatusError Status = "error"
)

// Span is the minimal tracing contract used by the runtime. Exporters for
// OTLP, logs, or vendor SDKs can implement this without becoming a core
// dependency.
type Span interface {
	SetAttributes(...Attribute)
	AddEvent(string, ...Attribute)
	SetStatus(Status, string)
	RecordError(error)
	End()
}

// Tracer creates spans. The returned context is the parent for nested spans.
type Tracer interface {
	Start(context.Context, string, ...Attribute) (context.Context, Span)
}

// NoopTracer has no allocation or exporter dependency.
type NoopTracer struct{}

func (NoopTracer) Start(ctx context.Context, _ string, _ ...Attribute) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopSpan struct{}

func (noopSpan) SetAttributes(...Attribute)    {}
func (noopSpan) AddEvent(string, ...Attribute) {}
func (noopSpan) SetStatus(Status, string)      {}
func (noopSpan) RecordError(error)             {}
func (noopSpan) End()                          {}

// SpanData is an immutable-on-read representation suitable for tests and
// simple exporters. ParentID is empty for root spans.
type SpanData struct {
	ID         uint64        `json:"id"`
	ParentID   uint64        `json:"parent_id,omitempty"`
	Name       string        `json:"name"`
	StartedAt  time.Time     `json:"started_at"`
	EndedAt    time.Time     `json:"ended_at,omitempty"`
	Duration   time.Duration `json:"duration,omitempty"`
	Status     Status        `json:"status"`
	StatusText string        `json:"status_text,omitempty"`
	Attributes []Attribute   `json:"attributes,omitempty"`
	Events     []SpanEvent   `json:"events,omitempty"`
	Error      string        `json:"error,omitempty"`
}

// SpanEvent is a timestamped event within a span.
type SpanEvent struct {
	Name       string      `json:"name"`
	Timestamp  time.Time   `json:"timestamp"`
	Attributes []Attribute `json:"attributes,omitempty"`
}

type spanContextKey struct{}

// MemoryTracer is a concurrency-safe reference tracer useful for tests,
// embedded deployments, and adapters that flush completed spans elsewhere.
type MemoryTracer struct {
	mu     sync.Mutex
	nextID uint64
	spans  []*memorySpan
}

// NewMemoryTracer creates an in-memory tracer.
func NewMemoryTracer() *MemoryTracer { return &MemoryTracer{} }

func (t *MemoryTracer) Start(ctx context.Context, name string, attributes ...Attribute) (context.Context, Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil {
		return ctx, noopSpan{}
	}
	var parentID uint64
	if parent, ok := ctx.Value(spanContextKey{}).(*memorySpan); ok && parent != nil {
		parentID = parent.id
	}
	t.mu.Lock()
	t.nextID++
	span := &memorySpan{tracer: t, id: t.nextID, parentID: parentID, name: name, startedAt: time.Now().UTC(), status: StatusUnset, attributes: append([]Attribute(nil), attributes...)}
	t.spans = append(t.spans, span)
	t.mu.Unlock()
	return context.WithValue(ctx, spanContextKey{}, span), span
}

// Spans returns a stable snapshot of all spans created by the tracer.
func (t *MemoryTracer) Spans() []SpanData {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	data := make([]SpanData, len(t.spans))
	for i, span := range t.spans {
		data[i] = span.snapshotLocked()
	}
	return data
}

type memorySpan struct {
	tracer             *MemoryTracer
	id, parentID       uint64
	name               string
	startedAt, endedAt time.Time
	status             Status
	statusText         string
	attributes         []Attribute
	events             []SpanEvent
	err                string
	ended              bool
}

func (s *memorySpan) SetAttributes(attributes ...Attribute) {
	if s == nil || s.tracer == nil {
		return
	}
	s.tracer.mu.Lock()
	defer s.tracer.mu.Unlock()
	if !s.ended {
		s.attributes = append(s.attributes, attributes...)
	}
}
func (s *memorySpan) AddEvent(name string, attributes ...Attribute) {
	if s == nil || s.tracer == nil {
		return
	}
	s.tracer.mu.Lock()
	defer s.tracer.mu.Unlock()
	if !s.ended {
		s.events = append(s.events, SpanEvent{Name: name, Timestamp: time.Now().UTC(), Attributes: append([]Attribute(nil), attributes...)})
	}
}
func (s *memorySpan) SetStatus(status Status, text string) {
	if s == nil || s.tracer == nil {
		return
	}
	s.tracer.mu.Lock()
	defer s.tracer.mu.Unlock()
	if !s.ended {
		s.status, s.statusText = status, text
	}
}
func (s *memorySpan) RecordError(err error) {
	if err == nil {
		return
	}
	s.SetStatus(StatusError, err.Error())
	if s == nil || s.tracer == nil {
		return
	}
	s.tracer.mu.Lock()
	defer s.tracer.mu.Unlock()
	if !s.ended {
		s.err = err.Error()
	}
}
func (s *memorySpan) End() {
	if s == nil || s.tracer == nil {
		return
	}
	s.tracer.mu.Lock()
	defer s.tracer.mu.Unlock()
	if s.ended {
		return
	}
	s.ended, s.endedAt = true, time.Now().UTC()
	if s.status == StatusUnset {
		s.status = StatusOK
	}
}
func (s *memorySpan) snapshotLocked() SpanData {
	data := SpanData{ID: s.id, ParentID: s.parentID, Name: s.name, StartedAt: s.startedAt, EndedAt: s.endedAt, Status: s.status, StatusText: s.statusText, Error: s.err, Attributes: append([]Attribute(nil), s.attributes...), Events: append([]SpanEvent(nil), s.events...)}
	if !s.endedAt.IsZero() {
		data.Duration = s.endedAt.Sub(s.startedAt)
	}
	return data
}
