package telemetry

import publictelemetry "github.com/diasYuri/y/pkg/telemetry"

// The internal package keeps compatibility for the binary's exporter while
// the canonical SDK contracts live in pkg/telemetry.
type Emitter = publictelemetry.Emitter
type NoopEmitter = publictelemetry.NoopEmitter
type BufferedEmitter = publictelemetry.BufferedEmitter

func NewBufferedEmitter(capacity int) *BufferedEmitter {
	return publictelemetry.NewBufferedEmitter(capacity)
}
