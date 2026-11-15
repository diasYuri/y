package component

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ComponentGeneration identifies one active instance captured by a runtime
// snapshot.
type ComponentGeneration struct {
	ID         string `json:"id"`
	Version    string `json:"version,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Generation uint64 `json:"generation"`
}

// ServiceBindingInfo describes a provider binding captured by a runtime
// snapshot. It contains identity and version metadata, not the service value.
type ServiceBindingInfo struct {
	ServiceID  string `json:"service_id"`
	ProviderID string `json:"provider_id"`
	Version    string `json:"version,omitempty"`
	Generation uint64 `json:"generation"`
}

// RuntimeSnapshot is an immutable view of one committed manager generation.
// It holds leases on the captured component scopes until Release is called.
type RuntimeSnapshot struct {
	id        string
	version   uint64
	instances map[string]*managedInstance
	services  map[serviceBinding]serviceValue
	release   sync.Once
}

// AcquireSnapshot captures the current committed components and services.
// A retired scope remains alive until every snapshot that contains it releases.
func (m *Manager) AcquireSnapshot() (*RuntimeSnapshot, error) {
	if m == nil {
		return nil, errors.New("component: manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closeWanted {
		return nil, ErrManagerClosed
	}
	instances := cloneInstances(m.active)
	acquired := make([]*managedInstance, 0, len(instances))
	for _, instance := range instances {
		if !instance.acquireSnapshot() {
			for _, held := range acquired {
				held.releaseSnapshot()
			}
			return nil, fmt.Errorf("component: active generation %q was already retired", instance.entry.descriptor.ID)
		}
		acquired = append(acquired, instance)
	}
	return &RuntimeSnapshot{
		id:        fmt.Sprintf("%s-%d", m.instanceID, m.viewVersion),
		version:   m.viewVersion,
		instances: instances,
		services:  cloneServices(m.services),
	}, nil
}

// ID returns the opaque identity of this committed view. It is process-local
// and intentionally changes when a manager is reconstructed after restore.
func (s *RuntimeSnapshot) ID() string {
	if s == nil {
		return ""
	}
	return s.id
}

// Version returns the manager-local commit number captured by this snapshot.
func (s *RuntimeSnapshot) Version() uint64 {
	if s == nil {
		return 0
	}
	return s.version
}

// Components returns active component IDs, revisions and generation numbers
// in stable ID order.
func (s *RuntimeSnapshot) Components() []ComponentGeneration {
	if s == nil {
		return nil
	}
	ids := make([]string, 0, len(s.instances))
	for id := range s.instances {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]ComponentGeneration, 0, len(ids))
	for _, id := range ids {
		descriptor := s.instances[id].entry.descriptor
		revision := descriptor.Revision
		if revision == "" {
			revision = descriptor.Version
		}
		out = append(out, ComponentGeneration{ID: id, Version: descriptor.Version, Revision: revision, Generation: s.instances[id].generation})
	}
	return out
}

// Services returns the service providers captured by this snapshot in stable
// service/provider order.
func (s *RuntimeSnapshot) Services() []ServiceBindingInfo {
	if s == nil {
		return nil
	}
	out := make([]ServiceBindingInfo, 0, len(s.services))
	for binding, value := range s.services {
		out = append(out, ServiceBindingInfo{
			ServiceID: binding.serviceID, ProviderID: binding.providerID,
			Version: value.version, Generation: binding.generation,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ServiceID != out[j].ServiceID {
			return out[i].ServiceID < out[j].ServiceID
		}
		return out[i].ProviderID < out[j].ProviderID
	})
	return out
}

// Owners returns the active component generation map for filtering scoped
// tools, hooks and context sources into the same view.
func (s *RuntimeSnapshot) Owners() map[string]uint64 {
	if s == nil {
		return nil
	}
	owners := make(map[string]uint64, len(s.instances))
	for id, instance := range s.instances {
		owners[id] = instance.generation
	}
	return owners
}

// ResolveService resolves a service from the provider generation captured by
// this snapshot.
func ResolveSnapshotService[T any](snapshot *RuntimeSnapshot, providerID string, key Key[T], constraint ...VersionConstraint) (T, error) {
	var zero T
	if snapshot == nil || providerID == "" || !validRef(key.ref) {
		return zero, fmt.Errorf("%w: invalid snapshot, provider, or service key", ErrServiceUnavailable)
	}
	if len(constraint) > 1 {
		return zero, errors.New("component: at most one service version constraint is allowed")
	}
	if len(constraint) == 1 {
		if err := constraint[0].Validate(); err != nil {
			return zero, err
		}
	}
	instance := snapshot.instances[providerID]
	if instance == nil {
		return zero, fmt.Errorf("%w: provider %q is not in snapshot", ErrServiceUnavailable, providerID)
	}
	binding := serviceBinding{serviceID: key.ref.ID, providerID: providerID, generation: instance.generation}
	value, exists := snapshot.services[binding]
	compatible := exists && value.version == key.ref.Version
	if exists && len(constraint) == 1 {
		compatible = constraint[0].Matches(value.version)
	}
	if !compatible {
		return zero, fmt.Errorf("%w: provider %q does not provide %q@%s in snapshot", ErrServiceUnavailable, providerID, key.ref.ID, key.ref.Version)
	}
	return unbox[T](value.value, key.ref)
}

// Release drops this snapshot's leases. It is safe to call more than once.
func (s *RuntimeSnapshot) Release() {
	if s == nil {
		return
	}
	s.release.Do(func() {
		for _, instance := range s.instances {
			instance.releaseSnapshot()
		}
	})
}

func cloneServices(services map[serviceBinding]serviceValue) map[serviceBinding]serviceValue {
	copy := make(map[serviceBinding]serviceValue, len(services))
	for binding, value := range services {
		copy[binding] = value
	}
	return copy
}
