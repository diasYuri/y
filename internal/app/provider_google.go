//go:build feature_google

package app

import (
	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/providers/google"
)

func newGoogleProvider(opts headlessOptions) (agent.Provider, error) {
	p := google.New()
	if opts.apiKey != "" {
		p = google.New(google.WithAPIKey(opts.apiKey))
	}
	return p, nil
}
