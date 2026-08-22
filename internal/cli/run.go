package cli

import (
	"io"

	"github.com/yuri/y/internal/app"
	"github.com/yuri/y/internal/buildinfo"
)

// Run executes the y command-line application.
func Run(stdout, stderr io.Writer, args []string, info buildinfo.Info) int {
	return app.Run(stdout, stderr, args, info)
}
