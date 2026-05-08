package openai

import (
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/providers/openai_compatible"
)

// newCompatible bridges openai.NewCompatible to the openai_compatible package.
// Most Option functions in this package configure fields shared with the
// compatible provider; we forward them via a per-option translator.
func newCompatible(baseURL string, opts ...Option) providers.Provider {
	// Probe the supplied openai.Options against a sentinel Provider to extract
	// the user-supplied configuration without coupling the openai_compatible
	// package to internal openai types.
	probe := &Provider{retry: providers.DefaultRetryPolicy()}
	for _, opt := range opts {
		opt(probe)
	}

	compatOpts := []openai_compatible.Option{
		openai_compatible.WithBaseURL(baseURL),
	}
	if probe.httpClient != nil {
		compatOpts = append(compatOpts, openai_compatible.WithHTTPClient(probe.httpClient))
	}
	if probe.apiKey != "" {
		compatOpts = append(compatOpts, openai_compatible.WithAPIKey(probe.apiKey))
	}
	if probe.envLookup != nil {
		compatOpts = append(compatOpts, openai_compatible.WithEnvLookup(probe.envLookup))
	}
	if probe.maxEvent > 0 {
		compatOpts = append(compatOpts, openai_compatible.WithMaxEventBytes(probe.maxEvent))
	}
	if probe.retry.MaxRetries != 0 || probe.retry.InitialDelay != 0 {
		compatOpts = append(compatOpts, openai_compatible.WithRetryPolicy(probe.retry))
	}
	for _, mw := range probe.middlewares {
		compatOpts = append(compatOpts, openai_compatible.WithMiddleware(mw))
	}
	if probe.inspector != nil {
		compatOpts = append(compatOpts, openai_compatible.WithRequestInspector(probe.inspector))
	}
	if probe.dryRun {
		compatOpts = append(compatOpts, openai_compatible.WithDryRun())
	}
	return openai_compatible.New(compatOpts...)
}
