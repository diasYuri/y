//go:build !feature_openai

package app

import "github.com/yuri/y/pkg/agent"

func newOpenAIProvider(opts headlessOptions) (agent.Provider, error) {
	return nil, newHeadlessError(exitCodeConfig, errProviderUnavailable("openai"))
}
