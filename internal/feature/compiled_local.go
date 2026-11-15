//go:build feature_local

package feature

func registerLocalIfCompiled(r *Registry) error {
	if err := r.AddProvider("local", "feature_local", "Local/OpenAI-compatible providers."); err != nil {
		return err
	}
	return r.AddComponent("provider.local", "feature_local", "Local provider component.")
}
