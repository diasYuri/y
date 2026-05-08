// Package wasm hosts the optional WASM extension subsystem.
//
// The host runtime is gated by the build tag feature_wasm_ext: builds without
// the tag still compile the package so that callers can reason about
// configuration and types, but Manager.Load returns ErrHostUnavailable instead
// of instantiating modules. This keeps the wazero dependency out of binaries
// that omit the tag.
//
// The Manager performs lazy loading: discovery and manifest validation run on
// demand, and modules are only instantiated the first time a tool call lands
// on them. Manifests are described by extension.toml files, with a small TOML
// subset compatible with the rest of the y configuration story.
package wasm
