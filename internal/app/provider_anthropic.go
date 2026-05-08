//go:build feature_anthropic

package app

import (
	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/providers/anthropic"
)

func newAnthropicProvider(opts headlessOptions) (agent.Provider, error) {
	p := anthropic.New()
	if opts.apiKey != "" {
		p = anthropic.New(anthropic.WithAPIKey(opts.apiKey))
	}
	return p, nil
}
