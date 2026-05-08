package telemetry

// Emitter receives telemetry events.
type Emitter interface {
	Emit(event Event)
}

// NoopEmitter discards all events.
type NoopEmitter struct{}

// Emit does nothing.
func (NoopEmitter) Emit(Event) {}

// BufferedEmitter batches events in memory until Flush is called.
type BufferedEmitter struct {
	buffer []Event
}

// NewBufferedEmitter creates a BufferedEmitter with the given capacity hint.
func NewBufferedEmitter(capacity int) *BufferedEmitter {
	return &BufferedEmitter{
		buffer: make([]Event, 0, capacity),
	}
}

// Emit appends an event to the buffer.
func (b *BufferedEmitter) Emit(event Event) {
	b.buffer = append(b.buffer, event)
}

// Flush returns all buffered events and clears the buffer.
func (b *BufferedEmitter) Flush() []Event {
	out := b.buffer
	b.buffer = nil
	return out
}

// Len returns the number of buffered events.
func (b *BufferedEmitter) Len() int {
	return len(b.buffer)
}
