package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/release"
)

func main() {
	var options release.Options
	flag.StringVar(&options.RepositoryRoot, "repository", "", "absolute repository root")
	flag.StringVar(&options.OutputDir, "output", "", "absolute new output directory")
	flag.StringVar(&options.Version, "version", "", "semantic product version")
	flag.StringVar(&options.Commit, "commit", "", "Git commit object ID")
	flag.Int64Var(&options.SourceEpoch, "source-date-epoch", 0, "reproducible build timestamp")
	flag.StringVar(&options.GoCommand, "go", "go", "Go command")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "build-release: positional arguments are not supported")
		os.Exit(2)
	}
	if options.RepositoryRoot == "" {
		current, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "build-release: resolve repository: %v\n", err)
			os.Exit(1)
		}
		options.RepositoryRoot = current
	}
	if absolute, err := filepath.Abs(options.RepositoryRoot); err == nil {
		options.RepositoryRoot = absolute
	}
	if options.OutputDir != "" {
		if absolute, err := filepath.Abs(options.OutputDir); err == nil {
			options.OutputDir = absolute
		}
	}
	manifest, err := release.Build(context.Background(), options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build-release: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(
		"built Memora %s (%s) release with %d artifacts at %s\n",
		manifest.ProductVersion, manifest.Commit, len(manifest.Artifacts), options.OutputDir,
	)
}
