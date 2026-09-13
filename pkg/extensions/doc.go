// Package extensions hosts the optional extension subsystems used by y.
//
// Subpackages:
//
//   - task-manager: native task planning and completion enforcement for Agent runs.
//   - subagents: native isolated child-agent orchestration with bounded execution.
//   - wasm: WASM extension host built on top of wazero. Gated by the
//     feature_wasm_ext build tag; builds without the tag still link a stub
//     Manager so callers can render extension listings.
//
// New extension hosts (e.g. native subprocess plugins) should live alongside
// wasm as their own subpackage so that build-tag gating remains local.
package extensions
