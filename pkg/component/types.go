package component

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrInvalidDescriptor  = errors.New("component: invalid descriptor")
	ErrDuplicateComponent = errors.New("component: duplicate component")
	ErrDuplicateProvider  = errors.New("component: duplicate service provider")
	ErrMissingService     = errors.New("component: required service has no provider")
	ErrVersionMismatch    = errors.New("component: service interface version mismatch")
	ErrDependencyCycle    = errors.New("component: dependency cycle")
	ErrInvalidFactory     = errors.New("component: invalid factory")
	ErrRuntimeState       = errors.New("component: invalid runtime state")
	ErrUndeclaredService  = errors.New("component: service is not declared by component")
	ErrServiceUnavailable = errors.New("component: service is not available")
	ErrServiceType        = errors.New("component: service value has a different Go type")
	ErrScopeClosed        = errors.New("component: scope is closed")
	ErrMissingProvision   = errors.New("component: declared provision was not installed")
	ErrAmbiguousService   = errors.New("component: service provider is ambiguous")
	ErrManagerClosed      = errors.New("component: manager is closed")
	ErrNotActive          = errors.New("component: instance is not active")
)

// ServiceRef identifies a service contract. Version is the version of the
// service interface, independent from the version of the component that
// provides it. The first runtime requires exact version matches.
type ServiceRef struct {
	ID      string
	Version string
}

// VersionConstraint restricts the semantic versions accepted for a service.
// Set Exact or a bounded range. Range lower bounds are inclusive and upper
// bounds are exclusive; an empty bound is unbounded.
type VersionConstraint struct {
	Exact        string
	MinInclusive string
	MaxExclusive string
}

// Validate checks that the constraint is a valid exact version or range.
func (c VersionConstraint) Validate() error {
	if c.Exact != "" {
		if c.MinInclusive != "" || c.MaxExclusive != "" {
			return errors.New("component: exact version cannot be combined with range bounds")
		}
		if _, err := parseSemanticVersion(c.Exact); err != nil {
			return fmt.Errorf("component: invalid exact service version: %w", err)
		}
		return nil
	}
	if c.MinInclusive == "" && c.MaxExclusive == "" {
		return errors.New("component: service version constraint is empty")
	}
	var min, max semanticVersion
	var err error
	if c.MinInclusive != "" {
		min, err = parseSemanticVersion(c.MinInclusive)
		if err != nil {
			return fmt.Errorf("component: invalid minimum service version: %w", err)
		}
	}
	if c.MaxExclusive != "" {
		max, err = parseSemanticVersion(c.MaxExclusive)
		if err != nil {
			return fmt.Errorf("component: invalid maximum service version: %w", err)
		}
	}
	if c.MinInclusive != "" && c.MaxExclusive != "" && compareSemanticVersions(min, max) >= 0 {
		return errors.New("component: service version range minimum must be below maximum")
	}
	return nil
}

// Matches reports whether version satisfies the validated constraint.
func (c VersionConstraint) Matches(version string) bool {
	parsed, err := parseSemanticVersion(version)
	if err != nil || c.Validate() != nil {
		return false
	}
	if c.Exact != "" {
		exact, _ := parseSemanticVersion(c.Exact)
		return compareSemanticVersions(parsed, exact) == 0
	}
	if c.MinInclusive != "" {
		min, _ := parseSemanticVersion(c.MinInclusive)
		if compareSemanticVersions(parsed, min) < 0 {
			return false
		}
	}
	if c.MaxExclusive != "" {
		max, _ := parseSemanticVersion(c.MaxExclusive)
		if compareSemanticVersions(parsed, max) >= 0 {
			return false
		}
	}
	return true
}

// ServiceRequirement binds a component to exactly one provider instance.
// ProviderID is the ID of the component instance that supplies ServiceID.
type ServiceRequirement struct {
	ServiceID   string
	ProviderID  string
	Version     VersionConstraint
	Cardinality RequirementCardinality
}

// RequirementCardinality describes how many providers satisfy a requirement.
type RequirementCardinality string

const (
	// CardinalityOne requires exactly one explicitly selected provider.
	CardinalityOne RequirementCardinality = "one"
)

// Capability describes an authority or feature a component requests. Y's
// component runtime records these declarations for inspection; a policy
// engine may enforce them at the relevant service boundary.
type Capability struct {
	ID          string
	Description string
}

// Descriptor is the immutable composition contract for one component
// instance. Component versions describe the implementation/configuration
// unit; service versions describe the interfaces it consumes and provides.
type Descriptor struct {
	ID      string
	Version string
	// Revision changes when runtime configuration changes without changing the
	// component implementation version. If empty, Version is used as the revision.
	Revision     string
	Requires     []ServiceRef
	Provides     []ServiceRef
	Requirements []ServiceRequirement
	Capabilities []Capability
}

// Validate checks descriptor fields and duplicate declarations.
func (d Descriptor) Validate() error {
	if d.ID == "" {
		return fmt.Errorf("%w: component ID is empty", ErrInvalidDescriptor)
	}
	if d.Version == "" {
		return fmt.Errorf("%w: component %q has no version", ErrInvalidDescriptor, d.ID)
	}
	if err := validateServiceRefs(d.ID, "requirement", d.Requires); err != nil {
		return err
	}
	if err := validateServiceRefs(d.ID, "provision", d.Provides); err != nil {
		return err
	}
	seenRequirements := make(map[string]struct{}, len(d.Requirements))
	for _, requirement := range d.Requirements {
		if requirement.ServiceID == "" || requirement.ProviderID == "" {
			return fmt.Errorf("%w: component %q has an incomplete service requirement", ErrInvalidDescriptor, d.ID)
		}
		if requirement.Cardinality != CardinalityOne {
			return fmt.Errorf("%w: component %q has unsupported cardinality %q", ErrInvalidDescriptor, d.ID, requirement.Cardinality)
		}
		if err := requirement.Version.Validate(); err != nil {
			return fmt.Errorf("%w: component %q requires %q from %q: %w", ErrInvalidDescriptor, d.ID, requirement.ServiceID, requirement.ProviderID, err)
		}
		if _, exists := seenRequirements[requirement.ServiceID]; exists {
			return fmt.Errorf("%w: component %q repeats service requirement %q", ErrInvalidDescriptor, d.ID, requirement.ServiceID)
		}
		seenRequirements[requirement.ServiceID] = struct{}{}
	}
	for _, requirement := range d.Requires {
		if _, exists := seenRequirements[requirement.ID]; exists {
			return fmt.Errorf("%w: component %q repeats service requirement %q", ErrInvalidDescriptor, d.ID, requirement.ID)
		}
	}
	seenCapabilities := make(map[string]struct{}, len(d.Capabilities))
	for _, capability := range d.Capabilities {
		if capability.ID == "" {
			return fmt.Errorf("%w: component %q has an empty capability ID", ErrInvalidDescriptor, d.ID)
		}
		if _, exists := seenCapabilities[capability.ID]; exists {
			return fmt.Errorf("%w: component %q repeats capability %q", ErrInvalidDescriptor, d.ID, capability.ID)
		}
		seenCapabilities[capability.ID] = struct{}{}
	}
	return nil
}

func validateServiceRefs(componentID, kind string, refs []ServiceRef) error {
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref.ID == "" || ref.Version == "" {
			return fmt.Errorf("%w: component %q has an incomplete %s", ErrInvalidDescriptor, componentID, kind)
		}
		if _, exists := seen[ref.ID]; exists {
			return fmt.Errorf("%w: component %q repeats %s %q", ErrInvalidDescriptor, componentID, kind, ref.ID)
		}
		seen[ref.ID] = struct{}{}
	}
	return nil
}

func cloneDescriptor(descriptor Descriptor) Descriptor {
	descriptor.Requires = append([]ServiceRef(nil), descriptor.Requires...)
	descriptor.Provides = append([]ServiceRef(nil), descriptor.Provides...)
	descriptor.Requirements = append([]ServiceRequirement(nil), descriptor.Requirements...)
	descriptor.Capabilities = append([]Capability(nil), descriptor.Capabilities...)
	return descriptor
}

// Key is a typed handle for a service interface. Share a package-level key
// between providers and consumers so both sides agree on its Go type and
// stable service identity.
type Key[T any] struct {
	ref ServiceRef
}

// NewKey creates a service key. Empty IDs or versions are rejected when the
// key is declared in a Descriptor or used through a Scope.
func NewKey[T any](id, interfaceVersion string) Key[T] {
	return Key[T]{ref: ServiceRef{ID: id, Version: interfaceVersion}}
}

// Reference returns the service identity suitable for a Descriptor.
func Reference[T any](key Key[T]) ServiceRef { return key.ref }

// Component is the lifecycle contract for an instantiated component. Long-
// lived resources and registrations must be recorded with Scope.Track during
// Start so Runtime can release them after partial failure or shutdown. Cleanup
// does not roll back external emissions or delete data owned by a durable store.
type Component interface {
	Start(context.Context, *Scope) error
}

// ComponentFunc adapts a function to Component.
type ComponentFunc func(context.Context, *Scope) error

// Start implements Component.
func (f ComponentFunc) Start(ctx context.Context, scope *Scope) error {
	if f == nil {
		return errors.New("component: nil start function")
	}
	return f(ctx, scope)
}

// Factory constructs one component instance. Factories run only after the
// entire composition has passed descriptor validation and must not perform
// externally visible side effects; Start and Scope own runtime effects.
type Factory func() (Component, error)

// Definition binds an inspectable descriptor to a factory. Configuration is
// normally captured by the factory closure using the component's typed config.
type Definition struct {
	Descriptor Descriptor
	Factory    Factory
}

// Define creates a definition and takes a defensive copy of descriptor.
func Define(descriptor Descriptor, factory Factory) Definition {
	return Definition{Descriptor: cloneDescriptor(descriptor), Factory: factory}
}

// Cleanup releases one resource or registration acquired during Start. It is
// not a generic undo for external operations, nor a request to delete durable
// state.
type Cleanup func(context.Context) error
