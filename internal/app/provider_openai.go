//go:build feature_openai

package app

import (
	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/providers/openai"
)

func newOpenAIProvider(opts headlessOptions) (agent.Provider, error) {
	p := openai.New()
	if opts.apiKey != "" {
		p = openai.New(openai.WithAPIKey(opts.apiKey))
	}
	return p, nil
}
