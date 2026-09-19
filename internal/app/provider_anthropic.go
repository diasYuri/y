//go:build feature_anthropic

package app

import (
	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/providers/anthropic"
)

func newAnthropicProvider(opts headlessOptions) (agent.Provider, error) {
	p := anthropic.New()
	if opts.apiKey != "" {
		p = anthropic.New(anthropic.WithAPIKey(opts.apiKey))
	}
	return p, nil
}
