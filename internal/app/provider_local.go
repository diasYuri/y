//go:build feature_local

package app

import (
	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/providers/openai_compatible"
)

func newLocalProvider(opts headlessOptions) (agent.Provider, error) {
	p := openai_compatible.New()
	if opts.apiKey != "" {
		p = openai_compatible.New(openai_compatible.WithAPIKey(opts.apiKey))
	}
	return p, nil
}
