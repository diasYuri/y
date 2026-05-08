//go:build !feature_pods

package feature

func registerPodsIfCompiled(r *Registry) error { return nil }
