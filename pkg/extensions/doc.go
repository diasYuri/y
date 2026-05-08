// Package extensions hosts the optional extension subsystems used by y.
//
// Subpackages:
//
//   - wasm: WASM extension host built on top of wazero. Gated by the
//     feature_wasm_ext build tag; builds without the tag still link a stub
//     Manager so callers can render extension listings.
//
// New extension hosts (e.g. native subprocess plugins) should live alongside
// wasm as their own subpackage so that build-tag gating remains local.
package extensions
