//go:build !feature_mom

package feature

func registerMomIfCompiled(r *Registry) error { return nil }
