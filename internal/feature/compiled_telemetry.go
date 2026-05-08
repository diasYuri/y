//go:build feature_telemetry

package feature

func registerTelemetryIfCompiled(r *Registry) error {
	return r.AddFeature("telemetry", "feature_telemetry", "Telemetry collection.")
}
