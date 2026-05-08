//go:build !feature_google

package feature

func registerGoogleIfCompiled(r *Registry) error { return nil }
