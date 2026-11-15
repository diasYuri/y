//go:build feature_google

package feature

func registerGoogleIfCompiled(r *Registry) error {
	if err := r.AddProvider("google", "feature_google", "Google provider."); err != nil {
		return err
	}
	return r.AddComponent("provider.google", "feature_google", "Google provider component.")
}
