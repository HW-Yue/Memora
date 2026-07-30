package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/acceptance"
)

func main() {
	var directory, publication, version, arch string
	flag.StringVar(&directory, "directory", "", "absolute acceptance output directory")
	flag.StringVar(&publication, "publication", "", "absolute publication directory")
	flag.StringVar(&version, "version", "", "stable semantic product version")
	flag.StringVar(&arch, "arch", "", "expected architecture")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal("positional arguments are not supported")
	}
	report, err := acceptance.Verify(
		absolute(directory),
		absolute(publication),
		version,
		arch,
	)
	if err != nil {
		fatal(err.Error())
	}
	fmt.Printf(
		"verified Memora %s darwin/%s clean-machine acceptance for %s\n",
		report.ProductVersion,
		report.Arch,
		report.Commit,
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
	fmt.Fprintln(os.Stderr, "verify clean-machine acceptance:", message)
	os.Exit(1)
}
