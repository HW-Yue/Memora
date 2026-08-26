package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/retrievaladapter"
	"github.com/HW-Yue/Memora/internal/retrievalscore"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("normalize-retrieval-suite", flag.ContinueOnError)
	flags.SetOutput(stderr)
	kind := flags.String("kind", "", "adapter kind: miracl-zh or mtrag")
	root := flags.String("root", "", "absolute downloaded dataset root")
	split := flags.String("split", "dev", "MIRACL split: dev or train")
	domain := flags.String("domain", "", "MTRAG domain: clapnq, cloud, fiqa or govt")
	queryMode := flags.String("query-mode", "lastturn", "MTRAG query mode: lastturn or rewrite")
	output := flags.String("output", "", "absolute normalized suite JSON output")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 || (*kind != "miracl-zh" && *kind != "mtrag") || !absoluteNormalized(*root) || !absoluteNormalized(*output) {
		_, _ = fmt.Fprintln(stderr, "normalize-retrieval-suite: kind, root and output are required; paths must be absolute normalized")
		return 2
	}
	if _, err := os.Lstat(*output); err == nil {
		_, _ = fmt.Fprintln(stderr, "normalize-retrieval-suite: output already exists")
		return 2
	} else if !os.IsNotExist(err) {
		return fail(stderr, err)
	}
	select {
	case <-ctx.Done():
		return fail(stderr, ctx.Err())
	default:
	}
	suite, err := retrievaladapter.Build(retrievaladapter.Config{Kind: *kind, Root: *root, Split: *split, Domain: *domain, QueryMode: *queryMode})
	if err != nil {
		return fail(stderr, err)
	}
	encoded, err := encodeSuite(suite)
	if err != nil {
		return fail(stderr, err)
	}
	if err := writeAtomic(*output, encoded); err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprintf(stdout, "Retrieval suite normalized: suite=%s queries=%d qrels=%d output=%s\n", suite.SuiteID, len(suite.Queries), len(suite.Qrels), *output)
	return 0
}

func encodeSuite(suite retrievalscore.Suite) ([]byte, error) {
	if err := suite.Validate(); err != nil {
		return nil, err
	}
	return retrievalscore.EncodeSuite(suite)
}

func absoluteNormalized(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func writeAtomic(path string, encoded []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".retrieval-suite-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func fail(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintln(stderr, "normalize-retrieval-suite:", err)
	return 1
}
