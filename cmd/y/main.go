package main

import (
	"os"

	"github.com/yuri/y/internal/app"
	"github.com/yuri/y/internal/buildinfo"
)

func main() {
	os.Exit(app.Run(os.Stdout, os.Stderr, os.Args[1:], buildinfo.Current()))
}
