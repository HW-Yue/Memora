package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/retrievalscore"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

type inputPaths []string

func (values *inputPaths) String() string { return fmt.Sprint([]string(*values)) }
func (values *inputPaths) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("build-retrieval-evaluation-report", flag.ContinueOnError)
	flags.SetOutput(stderr)
	suitePath := flags.String("suite", "", "absolute retrieval suite JSON path")
	outputPath := flags.String("output", "", "absolute report JSON output path")
	baseline := flags.String("baseline-run", "", "baseline run ID")
	k := flags.Int("k", 5, "Recall/Hit cutoff")
	var runPaths inputPaths
	flags.Var(&runPaths, "run", "absolute retrieval run JSON path; repeatable")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !absoluteNormalized(*suitePath) || !absoluteNormalized(*outputPath) || !validK(*k) || *baseline == "" || len(runPaths) == 0 {
		fmt.Fprintln(stderr, "build-retrieval-evaluation-report: suite/output/run/baseline required; paths must be absolute normalized and k must be positive")
		return 2
	}
	for _, path := range runPaths {
		if !absoluteNormalized(path) {
			fmt.Fprintln(stderr, "build-retrieval-evaluation-report: every run path must be absolute and normalized")
			return 2
		}
	}
	if _, err := os.Lstat(*outputPath); err == nil {
		fmt.Fprintln(stderr, "build-retrieval-evaluation-report: output already exists")
		return 2
	} else if !os.IsNotExist(err) {
		return fail(stderr, err)
	}
	select {
	case <-ctx.Done():
		return fail(stderr, ctx.Err())
	default:
	}
	suite, err := retrievalscore.LoadSuite(*suitePath)
	if err != nil {
		return fail(stderr, fmt.Errorf("load suite: %w", err))
	}
	runs := make([]retrievalscore.Run, 0, len(runPaths))
	for _, path := range runPaths {
		run, loadErr := retrievalscore.LoadRun(path)
		if loadErr != nil {
			return fail(stderr, fmt.Errorf("load run %s: %w", path, loadErr))
		}
		runs = append(runs, run)
	}
	report, err := retrievalscore.Evaluate(suite, runs, *baseline, *k)
	if err != nil {
		return fail(stderr, err)
	}
	encoded, err := retrievalscore.EncodeReport(report)
	if err != nil {
		return fail(stderr, err)
	}
	if err := writeAtomic(*outputPath, encoded); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "Retrieval evaluation report: suite=%s runs=%d k=%d output=%s\n", suite.SuiteID, len(runs), *k, *outputPath)
	return 0
}

func absoluteNormalized(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func validK(k int) bool { return k > 0 && k <= 10000 }

func writeAtomic(path string, encoded []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".retrieval-report-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func writeJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "build-retrieval-evaluation-report:", err)
	return 1
}
