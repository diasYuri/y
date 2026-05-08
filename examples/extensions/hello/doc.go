// Package hello holds a minimal example WASM extension that registers a
// single tool. The accompanying test verifies the example's manifest
// parses cleanly and that an ABI-compatible module can be loaded and
// invoked end-to-end through pkg/extensions/wasm.
//
// The test deliberately does not require TinyGo: it synthesises a WASM
// module with the same ABI surface using pkg/extensions/wasm/wasmtest so
// `go test ./...` keeps working on machines without TinyGo installed.
package hello
