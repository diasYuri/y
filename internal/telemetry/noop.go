//go:build !feature_telemetry

package telemetry

// DefaultEmitter returns the no-op emitter used when telemetry is not
// compiled in.
func DefaultEmitter() Emitter { return NoopEmitter{} }
