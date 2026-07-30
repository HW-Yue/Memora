package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/releasepublish"
)

func main() {
	var options releasepublish.BuildOptions
	flag.StringVar(&options.RepositoryRoot, "repository", "", "absolute repository snapshot root")
	flag.StringVar(&options.OutputDir, "output", "", "absolute new publication directory")
	flag.StringVar(&options.Version, "version", "", "stable semantic product version")
	flag.StringVar(&options.Commit, "commit", "", "Git commit object ID")
	flag.Int64Var(&options.SourceEpoch, "source-date-epoch", 0, "reproducible build timestamp")
	flag.StringVar(&options.GoCommand, "go", "go", "Go command")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal("positional arguments are not supported")
	}
	if options.RepositoryRoot == "" {
		current, err := os.Getwd()
		if err != nil {
			fatal(fmt.Sprintf("resolve repository: %v", err))
		}
		options.RepositoryRoot = current
	}
	options.RepositoryRoot = absolute(options.RepositoryRoot)
	options.OutputDir = absolute(options.OutputDir)
	publication, err := releasepublish.Build(context.Background(), options)
	if err != nil {
		fatal(err.Error())
	}
	fmt.Printf(
		"built Memora %s publication for %s with %d Skill files at %s\n",
		publication.Release.ProductVersion,
		publication.Release.Commit,
		len(publication.Bundle.Files),
		options.OutputDir,
	)
}

func absolute(value string) string {
	if value == "" {
		return ""
	}
	result, err := filepath.Abs(value)
	if err != nil {
		return value
	}
	return filepath.Clean(result)
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "build-publication:", message)
	os.Exit(1)
}
