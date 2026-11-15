//go:build feature_openai

package feature

func registerOpenAIIfCompiled(r *Registry) error {
	if err := r.AddProvider("openai", "feature_openai", "OpenAI provider."); err != nil {
		return err
	}
	return r.AddComponent("provider.openai", "feature_openai", "OpenAI provider component.")
}
