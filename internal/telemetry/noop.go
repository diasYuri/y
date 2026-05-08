//go:build !feature_telemetry

package telemetry

// DefaultEmitter is the no-op emitter used when telemetry is not compiled in.
var DefaultEmitter Emitter = NoopEmitter{}
