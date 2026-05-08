//go:build !feature_wasm_ext

package app

import (
	"fmt"
	"io"

	"github.com/yuri/y/internal/feature"
)

// runExtension reports that the WASM extension host is not part of this
// build. The function exists so the dispatch table in app.go can be
// build-tag-agnostic.
func runExtension(stdout, stderr io.Writer, args []string, info BuildInfo, compiled *feature.Registry) int {
	_ = stdout
	_ = args
	_ = info
	_ = compiled
	fmt.Fprintln(stderr, "y: extension commands are unavailable in this build (missing feature_wasm_ext)")
	return exitCodeUsage
}
