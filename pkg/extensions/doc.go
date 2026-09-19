// Package extensions hosts the optional extension subsystems used by y.
//
// Host is the transport-neutral composition boundary for installing tools,
// context sources, and lifecycle hooks into an agent runtime.
//
// Subpackages:
//
//   - task-manager: native task planning and completion enforcement for Agent runs.
//   - subagents: native isolated child-agent orchestration with bounded execution.
//   - memory: native historical-memory extension built on the public memory SDK.
//   - wasm: WASM extension host built on top of wazero. Gated by the
//     feature_wasm_ext build tag; builds without the tag still link a stub
//     Manager so callers can render extension listings.
//
// New extension hosts (e.g. native subprocess plugins) should live alongside
// wasm as their own subpackage so that build-tag gating remains local.
package extensions
