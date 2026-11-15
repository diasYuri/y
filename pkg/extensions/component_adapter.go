package extensions

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/diasYuri/y/pkg/component"
)

// AdaptLegacy wraps an existing Extension as a static component definition.
// The extension's ID is retained under the "extensions/" namespace and its
// version is marked "legacy" because the old Extension contract has no version
// or dependency declarations.
//
// The supplied Host must have the same lifetime as the component runtime.
// Legacy Host registrations do not expose per-extension removal handles, so
// this adapter is intended for startup/shutdown composition, not hot reload.
func AdaptLegacy(extension Extension, host *Host) (component.Definition, error) {
	if extension == nil {
		return component.Definition{}, errors.New("extensions: legacy extension is nil")
	}
	if host == nil {
		return component.Definition{}, errors.New("extensions: legacy host is nil")
	}
	id := extension.ID()
	if id == "" {
		return component.Definition{}, errors.New("extensions: legacy extension ID is empty")
	}
	descriptor := legacyDescriptor(id)
	if described, ok := extension.(ComponentDescriber); ok {
		descriptor = described.ComponentDescriptor()
		if descriptor.ID != "extensions/"+id {
			return component.Definition{}, fmt.Errorf("extensions: component descriptor ID must be %q", "extensions/"+id)
		}
		if _, scoped := extension.(ScopedInstaller); !scoped && (len(descriptor.Requires) != 0 || len(descriptor.Requirements) != 0 || len(descriptor.Provides) != 0) {
			return component.Definition{}, errors.New("extensions: service-aware extensions must implement ScopedInstaller")
		}
	}
	return adaptLegacyDescriptor(id, descriptor, host, func() (Extension, error) { return extension, nil })
}

// AdaptLegacyFactory creates a component definition without constructing the
// legacy extension before composition validation. The factory must be
// side-effect free and return an extension whose ID matches id.
func AdaptLegacyFactory(id string, host *Host, factory func() (Extension, error)) (component.Definition, error) {
	if id == "" {
		return component.Definition{}, errors.New("extensions: legacy extension ID is empty")
	}
	if host == nil {
		return component.Definition{}, errors.New("extensions: legacy host is nil")
	}
	if factory == nil {
		return component.Definition{}, errors.New("extensions: legacy extension factory is nil")
	}
	return adaptLegacyDescriptor(id, legacyDescriptor(id), host, factory)
}

func extensionDefinition(extension Extension) (ExtensionDefinition, error) {
	if extension == nil {
		return ExtensionDefinition{}, errors.New("extensions: extension is nil")
	}
	id := extension.ID()
	if id == "" {
		return ExtensionDefinition{}, errors.New("extensions: extension ID is empty")
	}
	descriptor := legacyDescriptor(id)
	if described, ok := extension.(ComponentDescriber); ok {
		descriptor = described.ComponentDescriptor()
		if descriptor.ID != "extensions/"+id {
			return ExtensionDefinition{}, fmt.Errorf("extensions: component descriptor ID must be %q", "extensions/"+id)
		}
	}
	serviceAware := descriptorHasServices(descriptor)
	if serviceAware {
		if _, ok := extension.(ScopedInstaller); !ok {
			return ExtensionDefinition{}, errors.New("extensions: service-aware extensions must implement ScopedInstaller")
		}
		factory, ok := extension.(ExtensionFactory)
		if !ok {
			return ExtensionDefinition{}, errors.New("extensions: service-aware extensions must implement ExtensionFactory")
		}
		return ExtensionDefinition{
			ID:         id,
			Descriptor: descriptor,
			Factory: func() (Extension, error) {
				fresh, err := factory.NewExtension()
				if err != nil {
					return nil, err
				}
				if fresh == nil || fresh.ID() != id {
					return nil, fmt.Errorf("extensions: factory must return extension %q", id)
				}
				if described, ok := fresh.(ComponentDescriber); !ok || !reflect.DeepEqual(described.ComponentDescriptor(), descriptor) {
					return nil, fmt.Errorf("extensions: factory for %q must preserve its component descriptor", id)
				}
				if _, ok := fresh.(ScopedInstaller); !ok {
					return nil, errors.New("extensions: service-aware extensions must implement ScopedInstaller")
				}
				return fresh, nil
			},
		}, nil
	}
	return ExtensionDefinition{
		ID:         id,
		Descriptor: descriptor,
		Factory:    func() (Extension, error) { return extension, nil },
	}, nil
}

func adaptExtensionDefinition(definition ExtensionDefinition, host *Host) (component.Definition, error) {
	if host == nil || definition.ID == "" || definition.Factory == nil {
		return component.Definition{}, errors.New("extensions: ID, host, and factory are required")
	}
	if definition.Descriptor.ID != "extensions/"+definition.ID {
		return component.Definition{}, fmt.Errorf("extensions: component descriptor ID must be %q", "extensions/"+definition.ID)
	}
	serviceAware := descriptorHasServices(definition.Descriptor)
	return component.Define(definition.Descriptor, func() (component.Component, error) {
		extension, err := definition.Factory()
		if err != nil {
			return nil, fmt.Errorf("construct extension %q: %w", definition.ID, err)
		}
		if extension == nil || extension.ID() != definition.ID {
			return nil, fmt.Errorf("extensions: factory must return extension %q", definition.ID)
		}
		if serviceAware {
			if _, ok := extension.(ScopedInstaller); !ok {
				return nil, errors.New("extensions: service-aware extensions must implement ScopedInstaller")
			}
			if described, ok := extension.(ComponentDescriber); !ok || !reflect.DeepEqual(described.ComponentDescriptor(), definition.Descriptor) {
				return nil, fmt.Errorf("extensions: factory for %q must preserve its component descriptor", definition.ID)
			}
		}
		return legacyComponent{extension: extension, host: host}, nil
	}), nil
}

func descriptorHasServices(descriptor component.Descriptor) bool {
	return len(descriptor.Requires) != 0 || len(descriptor.Requirements) != 0 || len(descriptor.Provides) != 0
}

func adaptLegacyDescriptor(id string, descriptor component.Descriptor, host *Host, factory func() (Extension, error)) (component.Definition, error) {
	if id == "" || host == nil || factory == nil {
		return component.Definition{}, errors.New("extensions: ID, host, and factory are required")
	}
	return component.Define(descriptor, func() (component.Component, error) {
		extension, err := factory()
		if err != nil {
			return nil, fmt.Errorf("construct legacy extension %q: %w", id, err)
		}
		if extension == nil || extension.ID() != id {
			return nil, fmt.Errorf("extensions: legacy factory must return extension %q", id)
		}
		return legacyComponent{extension: extension, host: host}, nil
	}), nil
}

func legacyDescriptor(id string) component.Descriptor {
	return component.Descriptor{
		ID:      "extensions/" + id,
		Version: "legacy",
		Capabilities: []component.Capability{{
			ID:          "y.legacy-extension",
			Description: "Uses the legacy extensions.Host registration surface",
		}},
	}
}

type legacyComponent struct {
	extension Extension
	host      *Host
}

func (c legacyComponent) Start(ctx context.Context, scope *component.Scope) error {
	if err := c.host.startExtension(ctx, c.extension, scope); err != nil {
		return fmt.Errorf("install legacy extension %q: %w", c.extension.ID(), err)
	}
	return nil
}
