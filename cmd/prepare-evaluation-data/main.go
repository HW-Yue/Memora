package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/evaldata"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

type datasetIDs []string

func (values *datasetIDs) String() string { return fmt.Sprint([]string(*values)) }
func (values *datasetIDs) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("prepare-evaluation-data", flag.ContinueOnError)
	flags.SetOutput(stderr)
	registryPath := flags.String("registry", "", "absolute frozen external evaluation registry JSON path")
	root := flags.String("root", os.Getenv("MEMORA_EVAL_ROOT"), "absolute external evaluation data root (or MEMORA_EVAL_ROOT)")
	verifyOnly := flags.Bool("verify-only", false, "verify already prepared data without network access")
	var selected datasetIDs
	flags.Var(&selected, "dataset", "dataset ID to prepare; repeatable, defaults to all")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !absoluteNormalized(*registryPath) || !absoluteNormalized(*root) {
		fmt.Fprintln(stderr, "prepare-evaluation-data: registry and root must be absolute normalized paths")
		return 2
	}
	registry, digest, err := evaldata.LoadRegistry(*registryPath)
	if err != nil {
		return fail(stderr, fmt.Errorf("load registry: %w", err))
	}
	receipt, err := evaldata.Prepare(ctx, registry, digest, evaldata.Options{
		Root:       *root,
		DatasetIDs: selected,
		VerifyOnly: *verifyOnly,
		Progress: func(progress evaldata.Progress) {
			if progress.State == "verified" || progress.State == "fetching" {
				fmt.Fprintf(stdout, "dataset=%s path=%s state=%s bytes=%d\n", progress.DatasetID, progress.Path, progress.State, progress.Bytes)
			}
		},
	})
	if err != nil {
		return fail(stderr, err)
	}
	verb := "prepared"
	if *verifyOnly {
		verb = "verified"
	}
	fmt.Fprintf(stdout, "External evaluation data %s: datasets=%d root=%s\n", verb, len(receipt.Datasets), *root)
	return 0
}

func absoluteNormalized(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "prepare-evaluation-data:", err)
	return 1
}
