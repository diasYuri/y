//go:build feature_anthropic

package feature

func registerAnthropicIfCompiled(r *Registry) error {
	if err := r.AddProvider("anthropic", "feature_anthropic", "Anthropic provider."); err != nil {
		return err
	}
	return r.AddComponent("provider.anthropic", "feature_anthropic", "Anthropic provider component.")
}
