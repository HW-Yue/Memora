package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/storygate"
)

func main() {
	binary := flag.String("binary", "", "absolute path to the memora release binary")
	dataDir := flag.String("data-dir", "", "absolute isolated Instance directory")
	canonical := flag.String("canonical-skill", "", "absolute canonical Skill directory")
	adapter := flag.String("adapter-dir", "", "absolute clean adapter install directory")
	host := flag.String("host", "", "codex or claude")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal(fmt.Errorf("positional arguments are not supported"))
	}
	report, err := storygate.Run(context.Background(), storygate.RunOptions{
		Binary: filepath.Clean(*binary), DataDir: filepath.Clean(*dataDir),
		CanonicalDir: filepath.Clean(*canonical), AdapterDir: filepath.Clean(*adapter), Host: *host,
	})
	encoded, marshalErr := json.MarshalIndent(report, "", "  ")
	if marshalErr == nil {
		fmt.Println(string(encoded))
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "run-story-gate:", err)
	os.Exit(1)
}
