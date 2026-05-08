//go:build !feature_local

package feature

func registerLocalIfCompiled(r *Registry) error { return nil }
