//go:build feature_google

package app

import (
	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/providers/google"
)

func newGoogleProvider(opts headlessOptions) (agent.Provider, error) {
	p := google.New()
	if opts.apiKey != "" {
		p = google.New(google.WithAPIKey(opts.apiKey))
	}
	return p, nil
}
