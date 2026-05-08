//go:build !feature_anthropic

package feature

func registerAnthropicIfCompiled(r *Registry) error { return nil }
