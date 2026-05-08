//go:build !feature_rpc

package app

import (
	"fmt"
	"io"
)

func runRPC(stdout, stderr io.Writer, args []string) int {
	fmt.Fprintln(stderr, "y rpc: RPC mode is not compiled into this binary. Build with -tags feature_rpc.")
	return 1
}
