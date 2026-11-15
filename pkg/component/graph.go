package component

import (
	"fmt"
	"sort"
)

type graphOptions struct {
	allowMissing           bool
	allowMultipleProviders bool
}

type graphNode struct {
	entry    runtimeEntry
	missing  []string
	eligible bool
}

type compiledGraph struct {
	ordered  []*graphNode
	inactive []*graphNode
}

func compileGraph(definitions []Definition, options graphOptions) (compiledGraph, error) {
	graph := compiledGraph{}
	nodes := make(map[string]*graphNode, len(definitions))
	providers := make(map[string]map[string]ServiceRef)
	for _, definition := range definitions {
		descriptor := cloneDescriptor(definition.Descriptor)
		if err := descriptor.Validate(); err != nil {
			return graph, err
		}
		if definition.Factory == nil {
			return graph, fmt.Errorf("%w: component %q has no factory", ErrInvalidFactory, descriptor.ID)
		}
		if _, exists := nodes[descriptor.ID]; exists {
			return graph, fmt.Errorf("%w: %q", ErrDuplicateComponent, descriptor.ID)
		}
		nodes[descriptor.ID] = &graphNode{entry: runtimeEntry{
			descriptor: descriptor,
			factory:    definition.Factory,
			bindings:   make(map[string]string),
		}}
		for _, provided := range descriptor.Provides {
			if options.allowMultipleProviders {
				if _, err := parseSemanticVersion(provided.Version); err != nil {
					return graph, fmt.Errorf("%w: component %q provides %q with invalid SemVer %q: %w", ErrInvalidDescriptor, descriptor.ID, provided.ID, provided.Version, err)
				}
			}
			byProvider := providers[provided.ID]
			if byProvider == nil {
				byProvider = make(map[string]ServiceRef)
				providers[provided.ID] = byProvider
			}
			if _, exists := byProvider[descriptor.ID]; exists {
				return graph, fmt.Errorf("%w: component %q repeats service %q", ErrDuplicateProvider, descriptor.ID, provided.ID)
			}
			byProvider[descriptor.ID] = provided
		}
	}

	if !options.allowMultipleProviders {
		for serviceID, byProvider := range providers {
			if len(byProvider) > 1 {
				providerIDs := sortedKeys(byProvider)
				return graph, fmt.Errorf("%w: service %q is declared by %q", ErrDuplicateProvider, serviceID, providerIDs)
			}
		}
	}

	adjacency := make(map[string]map[string]struct{}, len(nodes))
	for _, node := range nodes {
		for _, requirement := range node.entry.descriptor.Requires {
			byProvider := providers[requirement.ID]
			if len(byProvider) == 0 {
				node.missing = append(node.missing, requirement.ID+"@"+requirement.Version)
				continue
			}
			if len(byProvider) != 1 {
				return graph, fmt.Errorf("%w: component %q requires %q without selecting a provider", ErrAmbiguousService, node.entry.descriptor.ID, requirement.ID)
			}
			providerID, provided := onlyProvider(byProvider)
			if provided.Version != requirement.Version {
				return graph, fmt.Errorf("%w: component %q requires %q@%s, provider %q declares %q@%s", ErrVersionMismatch, node.entry.descriptor.ID, requirement.ID, requirement.Version, providerID, requirement.ID, provided.Version)
			}
			node.entry.bindings[requirement.ID] = providerID
			addDependency(adjacency, providerID, node.entry.descriptor.ID)
		}
		for _, requirement := range node.entry.descriptor.Requirements {
			providerNode := nodes[requirement.ProviderID]
			if providerNode == nil {
				node.missing = append(node.missing, requirement.ServiceID+" from "+requirement.ProviderID)
				continue
			}
			provided, exists := findProvided(providerNode.entry.descriptor.Provides, requirement.ServiceID)
			if !exists {
				return graph, fmt.Errorf("%w: component %q selects provider %q for service %q, but that provider does not declare it", ErrMissingService, node.entry.descriptor.ID, requirement.ProviderID, requirement.ServiceID)
			}
			if !requirement.Version.Matches(provided.Version) {
				return graph, fmt.Errorf("%w: component %q accepts %q from %q at %q, provider declares %q", ErrVersionMismatch, node.entry.descriptor.ID, requirement.ServiceID, requirement.ProviderID, describeConstraint(requirement.Version), provided.Version)
			}
			node.entry.bindings[requirement.ServiceID] = requirement.ProviderID
			addDependency(adjacency, requirement.ProviderID, node.entry.descriptor.ID)
		}
	}

	indegree := make(map[string]int, len(nodes))
	for id := range nodes {
		indegree[id] = 0
	}
	for providerID, consumers := range adjacency {
		for consumerID := range consumers {
			if nodes[providerID] == nil || nodes[consumerID] == nil {
				return graph, fmt.Errorf("%w: invalid dependency edge %q -> %q", ErrInvalidDescriptor, providerID, consumerID)
			}
			indegree[consumerID]++
		}
	}
	ready := make([]string, 0, len(nodes))
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	for len(ready) > 0 {
		current := ready[0]
		ready = ready[1:]
		node := nodes[current]
		node.eligible = len(node.missing) == 0
		for _, providerID := range node.entry.bindings {
			if provider := nodes[providerID]; provider != nil && !provider.eligible {
				node.eligible = false
				node.missing = append(node.missing, "provider "+providerID+" is inactive")
			}
		}
		sort.Strings(node.missing)
		graph.ordered = append(graph.ordered, node)
		for consumerID := range adjacency[current] {
			indegree[consumerID]--
			if indegree[consumerID] == 0 {
				ready = append(ready, consumerID)
				sort.Strings(ready)
			}
		}
	}
	if len(graph.ordered) != len(nodes) {
		cycle := make([]string, 0)
		for id, degree := range indegree {
			if degree > 0 {
				cycle = append(cycle, id)
			}
		}
		sort.Strings(cycle)
		return graph, fmt.Errorf("%w: %v", ErrDependencyCycle, cycle)
	}
	for _, node := range graph.ordered {
		if !node.eligible {
			graph.inactive = append(graph.inactive, node)
		}
	}
	if !options.allowMissing && len(graph.inactive) > 0 {
		node := graph.inactive[0]
		return graph, fmt.Errorf("%w: component %q: %v", ErrMissingService, node.entry.descriptor.ID, node.missing)
	}
	return graph, nil
}

func addDependency(adjacency map[string]map[string]struct{}, providerID, consumerID string) {
	if adjacency[providerID] == nil {
		adjacency[providerID] = make(map[string]struct{})
	}
	adjacency[providerID][consumerID] = struct{}{}
}

func onlyProvider(providers map[string]ServiceRef) (string, ServiceRef) {
	for id, provided := range providers {
		return id, provided
	}
	return "", ServiceRef{}
}

func findProvided(provided []ServiceRef, serviceID string) (ServiceRef, bool) {
	for _, ref := range provided {
		if ref.ID == serviceID {
			return ref, true
		}
	}
	return ServiceRef{}, false
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func describeConstraint(constraint VersionConstraint) string {
	if constraint.Exact != "" {
		return "=" + constraint.Exact
	}
	return fmt.Sprintf("[%s,%s)", constraint.MinInclusive, constraint.MaxExclusive)
}
