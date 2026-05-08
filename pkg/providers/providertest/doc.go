// Package providertest is the canonical location for in-memory provider
// implementations used by unit tests. The types here are thin re-exports of
// pkg/providers.FakeProvider; the implementation lives in pkg/providers to
// avoid an import cycle (providertest implements the Provider interface
// declared in pkg/providers).
//
// New tests should depend on pkg/providers/providertest so the test surface is
// not coupled to the production package layout.
package providertest

import (
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
)

// FakeProvider is the canonical in-memory provider for unit tests.
type FakeProvider = providers.FakeProvider

// FakeResponse is one queued streaming response.
type FakeResponse = providers.FakeResponse

// FakeOption configures a FakeProvider.
type FakeOption = providers.FakeOption

// NewFakeProvider creates a fake provider with one default text-capable model.
func NewFakeProvider(opts ...FakeOption) *FakeProvider {
	return providers.NewFakeProvider(opts...)
}

// WithFakeID sets the provider ID.
func WithFakeID(id string) FakeOption { return providers.WithFakeID(id) }

// WithFakeModels replaces the fake model list.
func WithFakeModels(models ...ai.Model) FakeOption {
	return providers.WithFakeModels(models...)
}

// WithFakeResponses queues responses returned by Stream.
func WithFakeResponses(responses ...FakeResponse) FakeOption {
	return providers.WithFakeResponses(responses...)
}

// WithFakeCapabilities overrides the capabilities returned by Capabilities.
func WithFakeCapabilities(c providers.Capabilities) FakeOption {
	return providers.WithFakeCapabilities(c)
}

// WithFakeCountTokens overrides the CountTokens implementation.
func WithFakeCountTokens(fn func(modelID string, c ai.Context) (int64, error)) FakeOption {
	return providers.WithFakeCountTokens(fn)
}
