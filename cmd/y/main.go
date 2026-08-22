package main

import (
	"os"

	"github.com/yuri/y/internal/buildinfo"
	"github.com/yuri/y/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Stdout, os.Stderr, os.Args[1:], buildinfo.Current()))
}
