package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/HW-Yue/Memora/internal/releasepublish"
)

func main() {
	var path string
	var tag string
	var version string
	flag.StringVar(&path, "json", "", "GitHub Release draft JSON")
	flag.StringVar(&tag, "tag", "", "expected tag")
	flag.StringVar(&version, "version", "", "expected product version")
	flag.Parse()
	if flag.NArg() != 0 || path == "" || tag == "" || version == "" {
		fmt.Fprintln(os.Stderr, "validate-release-draft: --json, --tag, and --version are required")
		os.Exit(2)
	}
	value, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate-release-draft: read response: %v\n", err)
		os.Exit(1)
	}
	if err := releasepublish.ValidateDraft(value, tag, version); err != nil {
		fmt.Fprintf(os.Stderr, "validate-release-draft: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("verified GitHub Release draft %s with %d assets\n", tag, len(releasepublish.AssetNames(version)))
}
