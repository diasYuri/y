//go:build feature_local

package feature

func registerLocalIfCompiled(r *Registry) error {
	return r.AddProvider("local", "feature_local", "Local/OpenAI-compatible providers.")
}
