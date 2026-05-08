//go:build feature_wasm_ext

package wasm

import (
	"github.com/yuri/y/pkg/extensions/wasm/wasmtest"
)

// buildABIToolModule returns a WASM module that satisfies the ABI by echoing
// a pre-baked tool response. It re-exports wasmtest.BuildABIToolModule so the
// existing tests keep their lowercase entry point.
func buildABIToolModule(response []byte) []byte {
	return wasmtest.BuildABIToolModule(response)
}

// buildABITrappingModule returns a guest whose pi_extension_handle traps
// using the unreachable opcode. The other ABI exports are valid so the
// loader still accepts the module.
func buildABITrappingModule() []byte {
	return wasmtest.BuildABITrappingModule()
}
