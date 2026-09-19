//go:build !feature_local

package app

import "github.com/diasYuri/y/pkg/agent"

func newLocalProvider(opts headlessOptions) (agent.Provider, error) {
	return nil, newHeadlessError(exitCodeConfig, errProviderUnavailable("local"))
}
