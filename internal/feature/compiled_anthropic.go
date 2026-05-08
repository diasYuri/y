//go:build feature_anthropic

package feature

func registerAnthropicIfCompiled(r *Registry) error {
	return r.AddProvider("anthropic", "feature_anthropic", "Anthropic provider.")
}
