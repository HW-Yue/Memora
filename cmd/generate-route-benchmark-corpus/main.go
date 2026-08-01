package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/routebenchmark"
)

func main() {
	if len(os.Args) != 2 || !filepath.IsAbs(os.Args[1]) {
		fmt.Fprintln(os.Stderr, "usage: generate-route-benchmark-corpus ABSOLUTE_OUTPUT")
		os.Exit(2)
	}
	corpus, err := routebenchmark.Generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate Route benchmark corpus:", err)
		os.Exit(1)
	}
	encoded, err := routebenchmark.Encode(corpus)
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode Route benchmark corpus:", err)
		os.Exit(1)
	}
	temporary := os.Args[1] + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write Route benchmark corpus:", err)
		os.Exit(1)
	}
	if err := os.Rename(temporary, os.Args[1]); err != nil {
		_ = os.Remove(temporary)
		fmt.Fprintln(os.Stderr, "publish Route benchmark corpus:", err)
		os.Exit(1)
	}
}
