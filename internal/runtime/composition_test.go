package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/diasYuri/y/internal/feature"
	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/ai"
	runtimeconfig "github.com/diasYuri/y/pkg/config"
	wasmext "github.com/diasYuri/y/pkg/extensions/wasm"
	"github.com/diasYuri/y/pkg/policy"
	"github.com/diasYuri/y/pkg/providers"
)

func TestValidateCompositionDoesNotConstructComponents(t *testing.T) {
	compiled := testCompositionCapabilities(t)
	config := testCompositionConfig()
	var calls atomic.Int32
	err := ValidateComposition(config, compiled, CompositionOptions{
		ProviderFactory: func(context.Context, string) (agent.Provider, error) {
			calls.Add(1)
			return &compositionTestProvider{}, nil
		},
	})
	if err != nil {
		t.Fatalf("ValidateComposition returned error: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("provider factory calls = %d, want 0", got)
	}
}

func TestGeneratedDefaultCompositionValidatesWithoutStarting(t *testing.T) {
	compiled := feature.NewRegistry()
	if err := feature.RegisterCompiledFeatures(compiled); err != nil {
		t.Fatalf("register compiled capabilities: %v", err)
	}
	config, err := runtimeconfig.Parse(strings.NewReader(runtimeconfig.GenerateDefault()))
	if err != nil {
		t.Fatalf("parse generated config: %v", err)
	}
	if err := ValidateComposition(config, compiled, CompositionOptions{}); err != nil {
		t.Fatalf("generated default composition is invalid: %v", err)
	}
}

func TestValidateCompositionRejectsMismatchedBindingBeforeConstruction(t *testing.T) {
	compiled := testCompositionCapabilities(t)
	config := testCompositionConfig()
	config.Bindings["agent_provider"] = "tools"
	var calls atomic.Int32
	err := ValidateComposition(config, compiled, CompositionOptions{
		ProviderFactory: func(context.Context, string) (agent.Provider, error) {
			calls.Add(1)
			return &compositionTestProvider{}, nil
		},
	})
	if err == nil {
		t.Fatal("ValidateComposition accepted a provider binding to tools.default")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("provider factory calls = %d after invalid binding, want 0", got)
	}
}

func TestValidateCompositionRejectsVersionAndCapabilityErrorsBeforeConstruction(t *testing.T) {
	compiled := testCompositionCapabilities(t)
	if err := compiled.AddComponent("extensions.host", "", "test capability"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	options := CompositionOptions{ProviderFactory: func(context.Context, string) (agent.Provider, error) {
		calls.Add(1)
		return &compositionTestProvider{}, nil
	}}

	versionConfig := testCompositionConfig()
	entry := versionConfig.Components["provider"]
	entry.Version = "2.0.0"
	versionConfig.Components["provider"] = entry
	if err := ValidateComposition(versionConfig, compiled, options); err == nil {
		t.Fatal("ValidateComposition accepted an unavailable component version")
	}

	capabilityConfig := testCompositionConfig()
	capabilityConfig.Components["extensions"] = runtimeconfig.ComponentConfig{Type: "extensions.host", Enabled: true, Version: builtinComponentVersion}
	capabilityConfig.Bindings["extensions_host"] = "extensions"
	capabilityConfig.Extensions = map[string]map[string]string{"wasm": {
		"enabled_ids": "missing-module", "allowed_capabilities": "unknown-capability",
	}}
	if err := ValidateComposition(capabilityConfig, compiled, options); err == nil {
		t.Fatal("ValidateComposition accepted invalid WASM capability configuration")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("provider factory calls = %d after invalid versions/capabilities, want 0", got)
	}
}

func TestValidateCompositionPreflightsSelectedWASMBeforeComponents(t *testing.T) {
	compiled := testCompositionCapabilities(t)
	if err := compiled.AddComponent("extensions.host", "", "test capability"); err != nil {
		t.Fatal(err)
	}
	config := testCompositionConfig()
	config.Components["extensions"] = runtimeconfig.ComponentConfig{Type: "extensions.host", Enabled: true, Version: builtinComponentVersion}
	config.Bindings["extensions_host"] = "extensions"
	config.Extensions = map[string]map[string]string{"wasm": {"enabled_ids": "not-installed"}}
	var calls atomic.Int32
	err := ValidateComposition(config, compiled, CompositionOptions{
		ProviderFactory: func(context.Context, string) (agent.Provider, error) {
			calls.Add(1)
			return &compositionTestProvider{}, nil
		},
	})
	if err == nil {
		t.Fatal("ValidateComposition accepted an unavailable selected WASM module")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("provider factory calls = %d before WASM preflight, want 0", got)
	}
}

func TestBuildCompositionClosesStartedComponentsWhenStartupIsCanceled(t *testing.T) {
	compiled := testCompositionCapabilities(t)
	config := testCompositionConfig()
	ctx, cancel := context.WithCancel(context.Background())
	provider := &compositionTestProvider{}
	composition, err := BuildComposition(ctx, config, compiled, CompositionOptions{
		ProviderFactory: func(context.Context, string) (agent.Provider, error) {
			cancel()
			return provider, nil
		},
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("BuildComposition error = %v, want context.Canceled", err)
	}
	if composition != nil {
		t.Fatal("BuildComposition returned a composition after canceled startup")
	}
	if got := provider.closed.Load(); got != 1 {
		t.Fatalf("provider close calls = %d, want 1 after rollback", got)
	}
}

func TestBuildCompositionResolvesAndClosesServices(t *testing.T) {
	compiled := testCompositionCapabilities(t)
	provider := &compositionTestProvider{}
	composition, err := BuildComposition(context.Background(), testCompositionConfig(), compiled, CompositionOptions{
		ProviderFactory: func(context.Context, string) (agent.Provider, error) { return provider, nil },
	})
	if err != nil {
		t.Fatalf("BuildComposition returned error: %v", err)
	}
	if composition.Provider != provider || composition.Registry == nil || composition.Telemetry == nil {
		t.Fatalf("composition services = provider %T, registry %v, telemetry %T", composition.Provider, composition.Registry, composition.Telemetry)
	}
	if err := composition.Close(context.Background()); err != nil {
		t.Fatalf("close composition: %v", err)
	}
	if got := provider.closed.Load(); got != 1 {
		t.Fatalf("provider close calls = %d, want 1", got)
	}
}

func testCompositionCapabilities(t *testing.T) *feature.Registry {
	t.Helper()
	compiled := feature.NewRegistry()
	for _, id := range []string{"provider.openai", "policy.default", "telemetry.default", "tools.default"} {
		if err := compiled.AddComponent(id, "", "test capability"); err != nil {
			t.Fatalf("register component %q: %v", id, err)
		}
	}
	return compiled
}

func testCompositionConfig() runtimeconfig.Config {
	return runtimeconfig.Config{
		CompositionExplicit: true,
		Components: map[string]runtimeconfig.ComponentConfig{
			"provider":  {Type: "provider.openai", Enabled: true, Version: builtinComponentVersion},
			"policy":    {Type: "policy.default", Enabled: true, Version: builtinComponentVersion},
			"telemetry": {Type: "telemetry.default", Enabled: true, Version: builtinComponentVersion},
			"tools":     {Type: "tools.default", Enabled: true, Version: builtinComponentVersion},
		},
		Bindings: map[string]string{
			"agent_provider": "provider", "policy_engine": "policy", "telemetry_emitter": "telemetry", "tools_registry": "tools",
		},
	}
}

type compositionTestProvider struct{ closed atomic.Int32 }

func (*compositionTestProvider) ID() string { return "composition-test" }
func (*compositionTestProvider) Models(context.Context) ([]ai.Model, error) {
	return nil, nil
}
func (*compositionTestProvider) Stream(context.Context, providers.StreamRequest) (providers.EventStream, error) {
	return nil, errors.New("unexpected Stream call")
}
func (*compositionTestProvider) CountTokens(context.Context, string, ai.Context) (int64, error) {
	return 1, nil
}
func (*compositionTestProvider) Capabilities(string) providers.Capabilities {
	return providers.Capabilities{}
}
func (p *compositionTestProvider) Close() error { p.closed.Add(1); return nil }

func TestWASMRuntimePolicy_UsesHarnessDecision(t *testing.T) {
	for _, kind := range []policy.DecisionKind{policy.DecisionAllow, policy.DecisionDeny, policy.DecisionRequireApproval} {
		t.Run(string(kind), func(t *testing.T) {
			adapter := wasmRuntimePolicy(policy.Func(func(_ context.Context, req policy.Request) (policy.Decision, error) {
				if req.Capability != "process.exec" || req.Identity.CallerID != "wasm.guest" || !req.Sensitive {
					t.Fatalf("request=%+v", req)
				}
				return policy.Decision{Kind: kind}, nil
			}))
			got, err := adapter.Authorize(context.Background(), wasmext.CapabilityRequest{ExtensionID: "guest", Capability: wasmext.CapProcessExec})
			want := wasmext.DecisionDeny
			if kind == policy.DecisionAllow {
				want = wasmext.DecisionAllow
			}
			if kind == policy.DecisionRequireApproval {
				want = wasmext.DecisionRequireApproval
			}
			if err != nil || got != want {
				t.Fatalf("decision=%v error=%v", got, err)
			}
		})
	}
}
