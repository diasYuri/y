//go:build feature_google

package feature

func registerGoogleIfCompiled(r *Registry) error {
	return r.AddProvider("google", "feature_google", "Google provider.")
}
