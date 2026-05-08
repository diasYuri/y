//go:build !feature_google

package app

import "github.com/yuri/y/pkg/agent"

func newGoogleProvider(opts headlessOptions) (agent.Provider, error) {
	return nil, newHeadlessError(exitCodeConfig, errProviderUnavailable("google"))
}
