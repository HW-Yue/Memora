package main

import (
	"os"

	"github.com/HW-Yue/Memora/internal/cli"
)

var (
	version = "dev"
	commit  = "unknown"
	builtAt = "unknown"
)

func main() {
	code := cli.Run(os.Args[1:], os.Stdout, os.Stderr, cli.BuildInfo{
		Version: version,
		Commit:  commit,
		BuiltAt: builtAt,
	})
	os.Exit(code)
}
