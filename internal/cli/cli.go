package cli

import (
	"encoding/json"
	"fmt"
	"io"
)

const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const helpText = `Memora is an AI-maintained local personal database.

Usage:
  memora <command> [options]

Commands:
  help       Show this help
  version    Show build version

Run 'memora help' for usage.
`

type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
}

type versionOutput struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
}

func Run(args []string, stdout, stderr io.Writer, build BuildInfo) int {
	if len(args) == 0 {
		return writeText(stdout, stderr, helpText)
	}

	switch args[0] {
	case "help", "-h", "--help":
		if len(args) != 1 {
			return usageError(stderr, "help does not accept arguments")
		}
		return writeText(stdout, stderr, helpText)
	case "version":
		return runVersion(args[1:], stdout, stderr, build)
	default:
		if _, err := fmt.Fprintf(stderr, "memora: unknown command %q\nRun 'memora help' for usage.\n", args[0]); err != nil {
			return ExitFailure
		}
		return ExitUsage
	}
}

func runVersion(args []string, stdout, stderr io.Writer, build BuildInfo) int {
	if len(args) == 0 {
		return writeText(stdout, stderr, fmt.Sprintf("memora %s (%s)\n", build.Version, build.Commit))
	}
	if len(args) != 1 || args[0] != "--json" {
		option := args[0]
		return usageError(stderr, fmt.Sprintf("unknown option for version: %q", option))
	}

	result := versionOutput{
		Name:    "memora",
		Version: build.Version,
		Commit:  build.Commit,
		BuiltAt: build.BuiltAt,
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return writeFailure(stderr, err)
	}
	return ExitOK
}

func usageError(stderr io.Writer, message string) int {
	if _, err := fmt.Fprintf(stderr, "memora: %s\n", message); err != nil {
		return ExitFailure
	}
	return ExitUsage
}

func writeText(stdout, stderr io.Writer, value string) int {
	if _, err := io.WriteString(stdout, value); err != nil {
		return writeFailure(stderr, err)
	}
	return ExitOK
}

func writeFailure(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintf(stderr, "memora: write output: %v\n", err)
	return ExitFailure
}
