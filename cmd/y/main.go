package main

import (
	"os"

	"github.com/diasYuri/y/internal/buildinfo"
	"github.com/diasYuri/y/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Stdout, os.Stderr, os.Args[1:], buildinfo.Current()))
}
