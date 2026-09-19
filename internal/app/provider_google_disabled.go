//go:build !feature_google

package app

import "github.com/diasYuri/y/pkg/agent"

func newGoogleProvider(opts headlessOptions) (agent.Provider, error) {
	return nil, newHeadlessError(exitCodeConfig, errProviderUnavailable("google"))
}
