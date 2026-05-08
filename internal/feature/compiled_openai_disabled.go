//go:build !feature_openai

package feature

func registerOpenAIIfCompiled(r *Registry) error { return nil }
