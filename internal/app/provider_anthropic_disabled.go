//go:build !feature_anthropic

package app

import "github.com/yuri/y/pkg/agent"

func newAnthropicProvider(opts headlessOptions) (agent.Provider, error) {
	return nil, newHeadlessError(exitCodeConfig, errProviderUnavailable("anthropic"))
}
