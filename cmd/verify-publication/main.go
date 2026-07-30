package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/releasepublish"
)

func main() {
	var directory string
	var version string
	flag.StringVar(&directory, "directory", "", "absolute publication directory")
	flag.StringVar(&version, "version", "", "expected stable semantic version")
	flag.Parse()
	if flag.NArg() != 0 || directory == "" || version == "" {
		fmt.Fprintln(os.Stderr, "verify-publication: --directory and --version are required")
		os.Exit(2)
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-publication: resolve directory: %v\n", err)
		os.Exit(1)
	}
	publication, err := releasepublish.Verify(filepath.Clean(absolute), version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-publication: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(
		"verified Memora %s publication for %s with %d release assets\n",
		publication.Release.ProductVersion,
		publication.Release.Commit,
		len(releasepublish.AssetNames(publication.Release.ProductVersion)),
	)
}
