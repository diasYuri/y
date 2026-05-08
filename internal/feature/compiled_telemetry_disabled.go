//go:build !feature_telemetry

package feature

func registerTelemetryIfCompiled(r *Registry) error { return nil }
