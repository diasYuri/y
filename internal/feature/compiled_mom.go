//go:build feature_mom

package feature

func registerMomIfCompiled(r *Registry) error {
	return r.AddFeature("mom", "feature_mom", "Slack automation product.")
}
