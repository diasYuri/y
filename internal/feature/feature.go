// Package feature tracks capabilities compiled into a y binary.
package feature

import (
	"fmt"
	"sort"
)

// Kind identifies a capability namespace.
type Kind string

const (
	KindFeature  Kind = "feature"
	KindProvider Kind = "provider"
	KindTool     Kind = "tool"
	KindCommand  Kind = "command"
)

// Descriptor describes a compiled or known capability.
type Descriptor struct {
	ID          string
	Kind        Kind
	BuildTag    string
	Description string
}

// Registry records capabilities compiled into the current binary.
type Registry struct {
	capabilities map[Kind]map[string]Descriptor
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{capabilities: make(map[Kind]map[string]Descriptor)}
}

// AddFeature registers a compiled runtime feature.
func (r *Registry) AddFeature(id, buildTag, description string) error {
	return r.Add(Descriptor{Kind: KindFeature, ID: id, BuildTag: buildTag, Description: description})
}

// AddProvider registers a compiled provider.
func (r *Registry) AddProvider(id, buildTag, description string) error {
	return r.Add(Descriptor{Kind: KindProvider, ID: id, BuildTag: buildTag, Description: description})
}

// AddTool registers a compiled tool.
func (r *Registry) AddTool(id, buildTag, description string) error {
	return r.Add(Descriptor{Kind: KindTool, ID: id, BuildTag: buildTag, Description: description})
}

// AddCommand registers a compiled CLI command.
func (r *Registry) AddCommand(id, description string) error {
	return r.Add(Descriptor{Kind: KindCommand, ID: id, Description: description})
}

// Add inserts a compiled capability descriptor.
func (r *Registry) Add(desc Descriptor) error {
	if desc.ID == "" {
		return fmt.Errorf("feature descriptor has empty id")
	}
	if desc.Kind == "" {
		return fmt.Errorf("feature descriptor %q has empty kind", desc.ID)
	}
	if !IsKnown(desc.Kind, desc.ID) {
		return fmt.Errorf("unknown %s %q", desc.Kind, desc.ID)
	}
	if r.capabilities == nil {
		r.capabilities = make(map[Kind]map[string]Descriptor)
	}
	if r.capabilities[desc.Kind] == nil {
		r.capabilities[desc.Kind] = make(map[string]Descriptor)
	}
	if _, exists := r.capabilities[desc.Kind][desc.ID]; exists {
		return fmt.Errorf("%s %q already registered", desc.Kind, desc.ID)
	}
	r.capabilities[desc.Kind][desc.ID] = desc
	return nil
}

// Has reports whether kind/id is compiled into the binary.
func (r *Registry) Has(kind Kind, id string) bool {
	if r == nil || r.capabilities == nil {
		return false
	}
	byKind := r.capabilities[kind]
	if byKind == nil {
		return false
	}
	_, ok := byKind[id]
	return ok
}

// Compiled returns registered capabilities in stable order.
func (r *Registry) Compiled() []Descriptor {
	if r == nil {
		return nil
	}
	var out []Descriptor
	for _, byKind := range r.capabilities {
		for _, desc := range byKind {
			out = append(out, desc)
		}
	}
	sortDescriptors(out)
	return out
}

// Status returns every known capability with its compiled state.
func (r *Registry) Status() []Status {
	known := Known()
	out := make([]Status, 0, len(known))
	for _, desc := range known {
		out = append(out, Status{
			Descriptor: desc,
			Compiled:   r.Has(desc.Kind, desc.ID),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Status is a known capability plus its compiled state.
type Status struct {
	Descriptor
	Compiled bool
}

func sortDescriptors(descs []Descriptor) {
	sort.Slice(descs, func(i, j int) bool {
		if descs[i].Kind != descs[j].Kind {
			return descs[i].Kind < descs[j].Kind
		}
		return descs[i].ID < descs[j].ID
	})
}
