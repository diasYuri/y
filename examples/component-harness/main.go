// Command component-harness demonstrates a harness-neutral component graph.
package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/component"
	ycontext "github.com/diasYuri/y/pkg/context"
	"github.com/diasYuri/y/pkg/providers"
	yruntime "github.com/diasYuri/y/pkg/runtime"
	"github.com/diasYuri/y/pkg/tools"
)

var (
	providerKey = component.NewKey[providers.Provider]("example.provider", "1")
	toolsKey    = component.NewKey[*tools.Registry]("example.tools", "1")
	contextKey  = component.NewKey[ycontext.ContextSource]("example.context", "1")
	harnessKey  = component.NewKey[Harness]("example.harness", "1")
)

// Harness gathers capabilities selected by one application composition.
type Harness struct {
	Provider      providers.Provider
	Tools         *tools.Registry
	ContextSource ycontext.ContextSource
}

func main() {
	ctx := context.Background()
	runtime, err := yruntime.NewHarness(infrastructureDefinition(), harnessDefinition())
	if err != nil {
		panic(err)
	}

	for _, descriptor := range runtime.Descriptors() {
		fmt.Printf("planned component: %s@%s\n", descriptor.ID, descriptor.Version)
	}
	if err := runtime.Start(ctx); err != nil {
		panic(err)
	}

	harness, err := yruntime.Resolve(runtime, harnessKey)
	if err != nil {
		panic(err)
	}
	items, err := harness.ContextSource.Load(ctx, ycontext.Request{})
	if err != nil {
		panic(err)
	}
	fmt.Printf("provider=%s tools=%d context_items=%d\n", harness.Provider.ID(), len(harness.Tools.List()), len(items))
	if err := runtime.Close(ctx); err != nil {
		panic(err)
	}
}

func infrastructureDefinition() component.Definition {
	descriptor := component.Descriptor{
		ID:      "example.infrastructure",
		Version: "1.0.0",
		Provides: []component.ServiceRef{
			component.Reference(providerKey),
			component.Reference(toolsKey),
			component.Reference(contextKey),
		},
		Capabilities: []component.Capability{{ID: "example.infrastructure"}},
	}
	return component.Define(descriptor, func() (component.Component, error) {
		return component.ComponentFunc(func(ctx context.Context, scope *component.Scope) error {
			registry := tools.NewRegistry()
			if err := scope.Track(registry.Close); err != nil {
				return err
			}
			provider := exampleProvider{}
			if err := scope.Track(func(context.Context) error { return provider.Close() }); err != nil {
				return err
			}
			if err := component.Provide(scope, toolsKey, registry); err != nil {
				return err
			}
			if err := component.Provide(scope, providerKey, providers.Provider(provider)); err != nil {
				return err
			}
			return component.Provide(scope, contextKey, ycontext.ContextSource(exampleContextSource{}))
		}), nil
	})
}

func harnessDefinition() component.Definition {
	descriptor := component.Descriptor{
		ID:      "example.harness",
		Version: "1.0.0",
		Requires: []component.ServiceRef{
			component.Reference(providerKey),
			component.Reference(toolsKey),
			component.Reference(contextKey),
		},
		Provides: []component.ServiceRef{component.Reference(harnessKey)},
	}
	return component.Define(descriptor, func() (component.Component, error) {
		return component.ComponentFunc(func(_ context.Context, scope *component.Scope) error {
			provider, err := component.Require(scope, providerKey)
			if err != nil {
				return err
			}
			registry, err := component.Require(scope, toolsKey)
			if err != nil {
				return err
			}
			source, err := component.Require(scope, contextKey)
			if err != nil {
				return err
			}
			return component.Provide(scope, harnessKey, Harness{
				Provider:      provider,
				Tools:         registry,
				ContextSource: source,
			})
		}), nil
	})
}

type exampleProvider struct{}

func (exampleProvider) ID() string { return "example" }
func (exampleProvider) Models(context.Context) ([]ai.Model, error) {
	return nil, nil
}
func (exampleProvider) Stream(context.Context, providers.StreamRequest) (providers.EventStream, error) {
	return nil, errors.New("example provider does not stream")
}
func (exampleProvider) CountTokens(context.Context, string, ai.Context) (int64, error) {
	return 1, nil
}
func (exampleProvider) Capabilities(string) providers.Capabilities { return providers.Capabilities{} }
func (exampleProvider) Close() error                               { return nil }

type exampleContextSource struct{}

func (exampleContextSource) Load(context.Context, ycontext.Request) ([]ycontext.Item, error) {
	return []ycontext.Item{{ID: "example", Content: "context from a component"}}, nil
}
