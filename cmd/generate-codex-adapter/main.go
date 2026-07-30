package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/codexadapter"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	canonical := filepath.Join(root, "skills", "memora")
	destination := filepath.Join(root, "adapters", "codex")
	if len(os.Args) == 3 {
		canonical, destination = os.Args[1], os.Args[2]
	} else if len(os.Args) != 1 {
		fatal(fmt.Errorf("usage: generate-codex-adapter [canonical-dir destination]"))
	}
	bundle, err := codexadapter.Build(canonical)
	if err != nil {
		fatal(err)
	}
	if err := bundle.Install(destination); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "generate Codex adapter:", err)
	os.Exit(1)
}
