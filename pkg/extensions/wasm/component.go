package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/component"
	extensionhost "github.com/diasYuri/y/pkg/extensions"
	"github.com/diasYuri/y/pkg/tools"
)

// ComponentDefinitions adapts discovered WASM manifests to the generic
// extension host. The manager remains responsible for sandboxing and capability
// grants; the host owns generation-scoped tool visibility and unload ordering.
func ComponentDefinitions(manager Manager, host *extensionhost.Host, ids ...string) ([]extensionhost.ExtensionDefinition, error) {
	if manager == nil || host == nil {
		return nil, errors.New("wasm components: manager and host are required")
	}
	definitions := make([]extensionhost.ExtensionDefinition, 0, len(ids))
	for _, id := range ids {
		info, err := manager.Get(id)
		if err != nil {
			return nil, fmt.Errorf("wasm component %q: %w", id, err)
		}
		manifest := info.Manifest
		componentID := "wasm." + id
		descriptor := descriptorForManifest(componentID, manifest)
		capturedID := id
		capturedManifest := manifest
		definitions = append(definitions, extensionhost.ExtensionDefinition{
			ID: componentID, Descriptor: descriptor,
			Factory: func() (extensionhost.Extension, error) {
				return &wasmComponent{manager: manager, host: host, id: capturedID, manifest: capturedManifest}, nil
			},
		})
	}
	return definitions, nil
}

type wasmComponent struct {
	manager  Manager
	host     *extensionhost.Host
	id       string
	manifest Manifest
}

func (e *wasmComponent) ID() string { return "wasm." + e.id }

func (e *wasmComponent) ComponentDescriptor() component.Descriptor {
	return descriptorForManifest(e.ID(), e.manifest)
}

func descriptorForManifest(id string, manifest Manifest) component.Descriptor {
	descriptor := component.Descriptor{ID: "extensions/" + id, Version: manifest.Version}
	for _, capability := range flattenManifestCapabilities(manifest.Capabilities) {
		descriptor.Capabilities = append(descriptor.Capabilities, component.Capability{ID: string(capability)})
	}
	for _, requirement := range manifest.Requirements {
		descriptor.Requirements = append(descriptor.Requirements, component.ServiceRequirement{
			ServiceID: requirement.ServiceID, ProviderID: requirement.ProviderID,
			Version: component.VersionConstraint{Exact: requirement.Version}, Cardinality: component.CardinalityOne,
		})
	}
	for _, provision := range manifest.Provides {
		descriptor.Provides = append(descriptor.Provides, component.ServiceRef{ID: provision.ServiceID, Version: provision.Version})
	}
	return descriptor
}

// Service is a JSON-only bridge to a WASM component service. Guest memory and
// Go runtime values are never shared across this boundary.
type Service interface {
	Call(context.Context, string, json.RawMessage) (json.RawMessage, error)
}

// ServiceKey creates the typed key for a manifest-declared WASM service.
func ServiceKey(serviceID, version string) component.Key[Service] {
	return component.NewKey[Service](serviceID, version)
}

func (e *wasmComponent) NewExtension() (extensionhost.Extension, error) {
	return &wasmComponent{manager: e.manager, host: e.host, id: e.id, manifest: e.manifest}, nil
}

func (e *wasmComponent) Install(*extensionhost.Host) error {
	return errors.New("wasm components require a component-scoped install")
}

func (e *wasmComponent) InstallScoped(ctx context.Context, host *extensionhost.Host, scope *component.Scope) error {
	if e == nil || e.manager == nil || host == nil || scope == nil {
		return errors.New("wasm component, manager, host, and scope are required")
	}
	if err := e.manager.Load(ctx, e.id); err != nil {
		return fmt.Errorf("load WASM extension %q: %w", e.id, err)
	}
	if err := scope.Track(func(ctx context.Context) error {
		return e.manager.Unload(ctx, e.id)
	}); err != nil {
		_ = e.manager.Unload(context.Background(), e.id)
		return err
	}
	if err := scope.Track(func(ctx context.Context) error {
		return e.callLifecycle(ctx, "stop", nil)
	}); err != nil {
		return err
	}
	if err := e.callLifecycle(ctx, "start", nil); err != nil {
		return err
	}
	for _, provision := range e.manifest.Provides {
		service := wasmService{manager: e.manager, extensionID: e.id}
		if err := component.Provide(scope, ServiceKey(provision.ServiceID, provision.Version), Service(service)); err != nil {
			return fmt.Errorf("provide WASM service %q: %w", provision.ServiceID, err)
		}
	}
	for _, declared := range e.manifest.Tools {
		toolName := "wasm_" + strings.ReplaceAll(e.id, ".", "_") + "_" + declared.Name
		name := declared.Name
		inputSchema := append(json.RawMessage(nil), declared.InputSchema...)
		outputSchema := append(json.RawMessage(nil), declared.OutputSchema...)
		if len(inputSchema) == 0 {
			inputSchema = json.RawMessage(`{"type":"object"}`)
		}
		descriptor := tools.ToolDescriptor{Name: toolName, Description: declared.Description, InputSchema: inputSchema}
		for _, capability := range flattenManifestCapabilities(e.manifest.Capabilities) {
			descriptor.Capabilities = append(descriptor.Capabilities, tools.Capability(capability))
		}
		if _, err := host.RegisterTool(scope, descriptor, tools.ToolHandlerFunc(func(callCtx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
			response, err := e.manager.CallTool(callCtx, e.id, ToolRequest{Tool: name, Arguments: req.Arguments, RequestID: req.ID})
			if err != nil {
				return tools.ToolResponse{}, fmt.Errorf("WASM extension %q tool %q: %w", e.id, name, err)
			}
			if len(outputSchema) > 0 {
				payload, err := json.Marshal(ToolCallResponse(response))
				if err != nil {
					return tools.ToolResponse{}, fmt.Errorf("WASM extension %q tool %q: encode output for schema validation: %w", e.id, name, err)
				}
				if err := tools.ValidateArguments(payload, outputSchema); err != nil {
					return tools.ToolResponse{}, fmt.Errorf("WASM extension %q tool %q returned output that violates its schema: %w", e.id, name, err)
				}
			}
			out := tools.ToolResponse{}
			for _, block := range response.Content {
				out.Content = append(out.Content, tools.ContentBlock{Type: tools.ContentType(block.Type), Text: block.Text, Details: append(json.RawMessage(nil), block.Data...)})
			}
			return out, nil
		})); err != nil {
			return fmt.Errorf("register WASM tool %q: %w", toolName, err)
		}
	}
	if _, err := host.RegisterHooks(scope, agent.RuntimeHooks{
		OnSession: func(hookCtx context.Context, event agent.SessionEvent) error {
			return e.callLifecycle(hookCtx, string(event.State), event)
		},
		BeforeTurn: func(hookCtx context.Context, turn agent.TurnContext) error {
			return e.callLifecycle(hookCtx, "before_turn", turn)
		},
		AfterTurn: func(hookCtx context.Context, turn agent.TurnContext, _ agent.RunResult) error {
			return e.callLifecycle(hookCtx, "after_turn", turn)
		},
	}); err != nil {
		return fmt.Errorf("register WASM lifecycle hooks: %w", err)
	}
	return nil
}

type wasmService struct {
	manager     Manager
	extensionID string
}

func (s wasmService) Call(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error) {
	request, err := json.Marshal(HookRequest{Name: method, Payload: append(json.RawMessage(nil), payload...)})
	if err != nil {
		return nil, err
	}
	response, err := s.manager.CallRuntime(ctx, s.extensionID, RuntimeRequest{Kind: KindHook, Payload: request})
	if err != nil {
		return nil, fmt.Errorf("WASM service %q method %q: %w", s.extensionID, method, err)
	}
	return append(json.RawMessage(nil), response.Payload...), nil
}

func (e *wasmComponent) callLifecycle(ctx context.Context, event string, value any) error {
	identity, _ := agent.RuntimeIdentityFromContext(ctx)
	var turn agent.TurnContext
	if parsed, ok := value.(agent.TurnContext); ok {
		turn = parsed
	}
	var session agent.SessionEvent
	if parsed, ok := value.(agent.SessionEvent); ok {
		session = parsed
	}
	if event == "start" || event == "stop" {
		session = agent.SessionEvent{}
	}
	payload, err := json.Marshal(struct {
		Event         string `json:"event"`
		RunID         string `json:"run_id,omitempty"`
		SessionID     string `json:"session_id,omitempty"`
		Turn          int    `json:"turn,omitempty"`
		RuntimeViewID string `json:"runtime_view_id,omitempty"`
		Runtime       any    `json:"runtime,omitempty"`
	}{Event: event, RunID: firstNonEmpty(identity.RunID, session.RunID, turn.RunID), SessionID: firstNonEmpty(session.SessionID, turn.SessionID), Turn: turn.Turn, RuntimeViewID: turn.RuntimeViewID, Runtime: value})
	if err != nil {
		return err
	}
	_, err = e.manager.CallRuntime(ctx, e.id, RuntimeRequest{Kind: KindLifecycle, Payload: payload})
	if err != nil {
		return fmt.Errorf("WASM extension %q lifecycle %q (run %q turn %d): %w", e.id, event, identity.RunID, turn.Turn, err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (e *wasmComponent) Close(ctx context.Context) error {
	return nil // scoped cleanup sends stop before unloading the module
}
