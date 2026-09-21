package main

import (
	"os"
	"runtime/debug"

	"github.com/HW-Yue/Memora/internal/cli"
)

var (
	version = "dev"
	commit  = "unknown"
	builtAt = "unknown"
)

// identify fills in what the linker did not.
//
// A release build passes these through -ldflags, but a plain `go build` leaves
// them at dev/unknown — and then nothing can tell two local builds apart, which
// is exactly the comparison a CLI needs to notice that it and the daemon came
// from different sources. The VCS stamp Go embeds is enough to answer it.
func identify() (string, string) {
	revision, builtAt, modified := "", "", false
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return commit, builtAt
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.time":
			builtAt = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return commit, builtAt
	}
	if modified {
		// A dirty tree is a different build, and saying so is the point: two
		// builds of the same revision with different working trees are not the
		// same binary.
		revision += "-dirty"
	}
	return revision, builtAt
}

func main() {
	if commit == "unknown" {
		commit, builtAt = identify()
	}
	if builtAt == "unknown" {
		_, fallback := identify()
		builtAt = fallback
	}
	code := cli.Run(os.Args[1:], os.Stdout, os.Stderr, cli.BuildInfo{
		Version: version,
		Commit:  commit,
		BuiltAt: builtAt,
	})
	os.Exit(code)
}
