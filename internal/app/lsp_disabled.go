//go:build !feature_lsp

package app

import (
	"fmt"
	"io"
)

func runLSP(stdout, stderr io.Writer, args []string) int {
	fmt.Fprintln(stderr, "y lsp: LSP integration is not compiled into this binary. Build with -tags feature_lsp.")
	return 1
}
