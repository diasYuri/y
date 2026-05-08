//go:build feature_rpc

package rpc

import (
	"sync"
	"time"
)

// StreamEvent is a real-time event emitted during agent execution.
type StreamEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Data      any    `json:"data"`
	Timestamp int64  `json:"timestamp"`
}

// eventBus routes stream events to subscribers.
type eventBus struct {
	mu          sync.RWMutex
	subscribers map[chan StreamEvent]struct{}
}

func newEventBus() *eventBus {
	return &eventBus{
		subscribers: make(map[chan StreamEvent]struct{}),
	}
}

// Subscribe registers a new event channel. The caller must call Unsubscribe
// when done to prevent leaks.
func (b *eventBus) Subscribe() chan StreamEvent {
	ch := make(chan StreamEvent, 16)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a channel from the bus and closes it.
func (b *eventBus) Unsubscribe(ch chan StreamEvent) {
	b.mu.Lock()
	delete(b.subscribers, ch)
	b.mu.Unlock()
	close(ch)
}

// Emit broadcasts an event to all subscribers. Non-blocking: events are dropped
// if a subscriber's buffer is full.
func (b *eventBus) Emit(ev StreamEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subscribers {
		select {
		case ch <- ev:
		default:
			// Subscriber is slow; drop the event.
		}
	}
}

// EmitAll broadcasts multiple events sequentially.
func (b *eventBus) EmitAll(events []StreamEvent) {
	for _, ev := range events {
		b.Emit(ev)
	}
}

func newStreamEvent(eventType, sessionID string, data any) StreamEvent {
	return StreamEvent{
		Type:      eventType,
		SessionID: sessionID,
		Data:      data,
		Timestamp: time.Now().Unix(),
	}
}
