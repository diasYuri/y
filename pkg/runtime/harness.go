package runtime

import (
	"context"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/component"
	"github.com/diasYuri/y/pkg/extensions"
	"github.com/diasYuri/y/pkg/policy"
	"github.com/diasYuri/y/pkg/session"
	"github.com/diasYuri/y/pkg/telemetry"
	"github.com/diasYuri/y/pkg/tools"
)

const (
	// HarnessServiceVersion is the version of the public service contracts below.
	HarnessServiceVersion = "1.0.0"
)

var (
	// ProviderKey identifies the agent provider selected by a harness.
	ProviderKey = component.NewKey[agent.Provider]("agent_provider", HarnessServiceVersion)
	// PolicyKey identifies the policy engine shared by tools and extensions.
	PolicyKey = component.NewKey[policy.Engine]("policy_engine", HarnessServiceVersion)
	// TelemetryKey identifies the runtime telemetry emitter.
	TelemetryKey = component.NewKey[telemetry.Emitter]("telemetry_emitter", HarnessServiceVersion)
	// ToolRegistryKey identifies the registry consumed by agents and transports.
	ToolRegistryKey = component.NewKey[*tools.Registry]("tools_registry", HarnessServiceVersion)
	// ExtensionHostKey identifies the manager-backed extension facade.
	ExtensionHostKey = component.NewKey[*extensions.Host]("extensions_host", HarnessServiceVersion)
	// SessionStoreKey identifies the durable transcript store.
	SessionStoreKey = component.NewKey[session.Store]("sessions_store", HarnessServiceVersion)
)

// Harness owns one static component composition. Definitions are validated
// before factories run, and all started scopes are closed in reverse order.
type Harness struct {
	runtime *component.Runtime
}

// NewHarness validates definitions and builds a deterministic static harness.
// It does not create providers, open stores, or start components.
func NewHarness(definitions ...component.Definition) (*Harness, error) {
	runtime, err := component.NewRuntime(definitions...)
	if err != nil {
		return nil, err
	}
	return &Harness{runtime: runtime}, nil
}

// Start starts each component in dependency order.
func (h *Harness) Start(ctx context.Context) error {
	if h == nil {
		return component.ErrRuntimeState
	}
	return h.runtime.Start(ctx)
}

// Close shuts down started components in reverse dependency order.
func (h *Harness) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	return h.runtime.Close(ctx)
}

// Descriptors returns defensive copies in start order.
func (h *Harness) Descriptors() []component.Descriptor {
	if h == nil {
		return nil
	}
	return h.runtime.Descriptors()
}

// Resolve returns a running service from the harness.
func Resolve[T any](h *Harness, key component.Key[T]) (T, error) {
	if h == nil {
		var zero T
		return zero, component.ErrServiceUnavailable
	}
	return component.Resolve(h.runtime, key)
}

// ResolveFrom returns a running service from one explicitly selected component.
func ResolveFrom[T any](h *Harness, providerID string, key component.Key[T]) (T, error) {
	if h == nil {
		var zero T
		return zero, component.ErrServiceUnavailable
	}
	return component.ResolveFrom(h.runtime, providerID, key)
}
