package wasm

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Capability identifies an action the host may grant to an extension. The
// canonical names match extension-wasm.md §15 so manifests, configs and
// host functions can speak the same vocabulary.
type Capability string

const (
	CapYTools          Capability = "y_tools"
	CapFilesystemRead  Capability = "filesystem.read"
	CapFilesystemWrite Capability = "filesystem.write"
	CapNetworkHTTP     Capability = "network.http"
	CapProcessExec     Capability = "process.exec"
	CapGitRead         Capability = "git.read"
	CapGitWrite        Capability = "git.write"
	CapSecretsRead     Capability = "secrets.read"
	CapStorage         Capability = "storage"
	CapLogs            Capability = "logs"
)

// AllCapabilities returns the set of capabilities the host knows how to
// validate. Anything outside this list is rejected as unknown.
func AllCapabilities() []Capability {
	return []Capability{
		CapYTools,
		CapFilesystemRead,
		CapFilesystemWrite,
		CapNetworkHTTP,
		CapProcessExec,
		CapGitRead,
		CapGitWrite,
		CapSecretsRead,
		CapStorage,
		CapLogs,
	}
}

// IsKnown reports whether c is a recognised capability name.
func (c Capability) IsKnown() bool {
	switch c {
	case CapYTools,
		CapFilesystemRead, CapFilesystemWrite,
		CapNetworkHTTP, CapProcessExec,
		CapGitRead, CapGitWrite,
		CapSecretsRead, CapStorage, CapLogs:
		return true
	}
	return false
}

// CapabilityRequest is the input to Policy.Authorize.
type CapabilityRequest struct {
	ExtensionID string
	Capability  Capability
	// Kind names the host call kind that prompted the authorisation
	// check. It lets policy implementations log a single line per
	// guarded host function call.
	Kind EnvelopeKind
	// Detail carries call-specific data (e.g. a tool name or filesystem
	// path). Implementations may ignore it, but logs benefit from the
	// extra context.
	Detail string
}

// Decision is the policy outcome.
type Decision int

const (
	DecisionDeny Decision = iota
	DecisionAllow
	// DecisionRequireApproval signals the caller must surface a UI
	// approval before continuing. Phase 8 only honours allow/deny; the
	// constant exists so future phases can extend the policy without
	// breaking the interface.
	DecisionRequireApproval
)

// Policy is the host-side gate consulted before any capability-bearing
// host function executes.
type Policy interface {
	Authorize(ctx context.Context, req CapabilityRequest) (Decision, error)
}

// PolicyFunc adapts a function into a Policy. Useful for tests.
type PolicyFunc func(context.Context, CapabilityRequest) (Decision, error)

// Authorize implements Policy.
func (f PolicyFunc) Authorize(ctx context.Context, req CapabilityRequest) (Decision, error) {
	return f(ctx, req)
}

// CapabilityGrantSet is the resolved view used at runtime. It is computed
// from the manifest declaration and the host configuration; the policy can
// still revoke individual decisions per-call.
type CapabilityGrantSet struct {
	values map[Capability]bool
}

// NewCapabilityGrantSet builds a deny-by-default grant set from the
// supplied capabilities.
func NewCapabilityGrantSet(caps ...Capability) CapabilityGrantSet {
	g := CapabilityGrantSet{values: make(map[Capability]bool, len(caps))}
	for _, c := range caps {
		if c.IsKnown() {
			g.values[c] = true
		}
	}
	return g
}

// Grant returns true when c was granted at startup.
func (g CapabilityGrantSet) Grant(c Capability) bool {
	if g.values == nil {
		return false
	}
	return g.values[c]
}

// List returns the granted capabilities in deterministic order.
func (g CapabilityGrantSet) List() []Capability {
	if g.values == nil {
		return nil
	}
	out := make([]Capability, 0, len(g.values))
	for c, ok := range g.values {
		if ok {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i]) < string(out[j]) })
	return out
}

// Strings returns the granted capabilities as []string for envelopes.
func (g CapabilityGrantSet) Strings() []string {
	caps := g.List()
	out := make([]string, len(caps))
	for i, c := range caps {
		out[i] = string(c)
	}
	return out
}

// ResolveCapabilityGrants converts the manifest's CapabilitySet into a
// CapabilityGrantSet, intersecting with the supplied host policy. The
// boolean toggles in the manifest fan out into the granular capability
// names defined in the spec.
func ResolveCapabilityGrants(req CapabilitySet, allowed []Capability) CapabilityGrantSet {
	manifest := flattenManifestCapabilities(req)
	allowSet := make(map[Capability]bool, len(allowed))
	for _, c := range allowed {
		if c.IsKnown() {
			allowSet[c] = true
		}
	}
	out := make([]Capability, 0, len(manifest))
	for _, c := range manifest {
		if allowSet[c] {
			out = append(out, c)
		}
	}
	return NewCapabilityGrantSet(out...)
}

// ParseCapabilities expands the legacy "filesystem"/"network"/... names
// understood by the manifest into the granular capability identifiers.
func ParseCapabilities(names []string) ([]Capability, error) {
	if len(names) == 0 {
		return nil, nil
	}
	out := make([]Capability, 0, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		c := Capability(name)
		if c.IsKnown() {
			out = append(out, c)
			continue
		}
		switch name {
		case "filesystem":
			out = append(out, CapFilesystemRead, CapFilesystemWrite)
		case "network":
			out = append(out, CapNetworkHTTP)
		case "process":
			out = append(out, CapProcessExec)
		case "git":
			out = append(out, CapGitRead, CapGitWrite)
		case "secrets":
			out = append(out, CapSecretsRead)
		case "pi_tools", "y_tools":
			out = append(out, CapYTools)
		default:
			return nil, fmt.Errorf("unknown capability %q", name)
		}
	}
	return out, nil
}

// flattenManifestCapabilities expands the boolean manifest representation
// into granular capability constants. Filesystem/git pull in both halves
// because the manifest does not split read and write today.
func flattenManifestCapabilities(s CapabilitySet) []Capability {
	out := make([]Capability, 0, 8)
	if s.YTools {
		out = append(out, CapYTools)
	}
	if s.Filesystem {
		out = append(out, CapFilesystemRead, CapFilesystemWrite)
	}
	if s.Network {
		out = append(out, CapNetworkHTTP)
	}
	if s.Process {
		out = append(out, CapProcessExec)
	}
	if s.Git {
		out = append(out, CapGitRead, CapGitWrite)
	}
	if s.Secrets {
		out = append(out, CapSecretsRead)
	}
	if s.Storage {
		out = append(out, CapStorage)
	}
	if s.Logs {
		out = append(out, CapLogs)
	}
	return out
}

// allowAllPolicy grants every capability. Useful in tests where the
// manifest has already restricted what the extension can ask for.
type allowAllPolicy struct{}

// AllowAllPolicy is a convenience policy used by callers that defer all
// gating to manifest-derived capability grants.
func AllowAllPolicy() Policy { return allowAllPolicy{} }

func (allowAllPolicy) Authorize(_ context.Context, _ CapabilityRequest) (Decision, error) {
	return DecisionAllow, nil
}

// denyAllPolicy refuses every request. The Manager falls back to it when no
// policy is configured.
type denyAllPolicy struct{}

// DenyAllPolicy refuses every capability request.
func DenyAllPolicy() Policy { return denyAllPolicy{} }

func (denyAllPolicy) Authorize(_ context.Context, _ CapabilityRequest) (Decision, error) {
	return DecisionDeny, nil
}
