//go:build feature_openai

package app

import (
	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/providers/openai"
)

func newOpenAIProvider(opts headlessOptions) (agent.Provider, error) {
	p := openai.New()
	if opts.apiKey != "" {
		p = openai.New(openai.WithAPIKey(opts.apiKey))
	}
	return p, nil
}
