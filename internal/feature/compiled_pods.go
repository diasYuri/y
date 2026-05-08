//go:build feature_pods

package feature

func registerPodsIfCompiled(r *Registry) error {
	return r.AddFeature("pods", "feature_pods", "Pods management product.")
}
