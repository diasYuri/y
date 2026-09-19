//go:build !feature_openai

package app

import "github.com/diasYuri/y/pkg/agent"

func newOpenAIProvider(opts headlessOptions) (agent.Provider, error) {
	return nil, newHeadlessError(exitCodeConfig, errProviderUnavailable("openai"))
}
