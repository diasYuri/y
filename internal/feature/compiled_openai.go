//go:build feature_openai

package feature

func registerOpenAIIfCompiled(r *Registry) error {
	return r.AddProvider("openai", "feature_openai", "OpenAI provider.")
}
