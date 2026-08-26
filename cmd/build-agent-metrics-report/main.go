package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/agentmetrics"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

type inputPaths []string

func (values *inputPaths) String() string { return fmt.Sprint([]string(*values)) }

func (values *inputPaths) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("build-agent-metrics-report", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var inputs inputPaths
	flags.Var(&inputs, "input", "absolute Hook snapshot JSON path; repeatable")
	jsonPath := flags.String("json", "", "absolute output report JSON path")
	htmlPath := flags.String("html", "", "optional absolute output report HTML path")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 || len(inputs) == 0 || !absolute(*jsonPath) || *htmlPath != "" && !absolute(*htmlPath) {
		_, _ = fmt.Fprintln(stderr, "build-agent-metrics-report: input and json require absolute normalized paths; html is optional")
		return 2
	}
	if *htmlPath == *jsonPath {
		_, _ = fmt.Fprintln(stderr, "build-agent-metrics-report: json and html outputs must be different")
		return 2
	}
	for _, path := range inputs {
		if !absolute(path) {
			_, _ = fmt.Fprintln(stderr, "build-agent-metrics-report: every input path must be absolute and normalized")
			return 2
		}
	}

	envelopes := make([]agent.ExternalAgentHookEnvelope, 0, len(inputs))
	for _, path := range inputs {
		select {
		case <-ctx.Done():
			return fail(stderr, ctx.Err())
		default:
		}
		encoded, err := os.ReadFile(path)
		if err != nil {
			return fail(stderr, fmt.Errorf("read %s: %w", path, err))
		}
		envelope, err := agentmetrics.DecodeHookJSON(encoded)
		if err != nil {
			return fail(stderr, fmt.Errorf("decode %s: %w", path, err))
		}
		envelopes = append(envelopes, envelope)
	}
	report, err := agentmetrics.Aggregate(envelopes)
	if err != nil {
		return fail(stderr, err)
	}
	encoded, err := agentmetrics.EncodeJSON(report)
	if err != nil {
		return fail(stderr, err)
	}
	if err := writeAtomic(*jsonPath, encoded); err != nil {
		return fail(stderr, fmt.Errorf("write %s: %w", *jsonPath, err))
	}
	if *htmlPath != "" {
		html, err := agentmetrics.RenderHTML(report)
		if err != nil {
			return fail(stderr, err)
		}
		if err := writeAtomic(*htmlPath, html); err != nil {
			return fail(stderr, fmt.Errorf("write %s: %w", *htmlPath, err))
		}
	}
	_, _ = fmt.Fprintf(stdout, "Agent metrics report: envelopes=%d events=%d json=%s", report.EnvelopeCount, report.EventCount, *jsonPath)
	if *htmlPath != "" {
		_, _ = fmt.Fprintf(stdout, " html=%s", *htmlPath)
	}
	_, _ = fmt.Fprintln(stdout)
	return 0
}

func absolute(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func writeAtomic(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()
	if _, err := temporary.Write(data); err != nil {
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
	_, _ = fmt.Fprintln(stderr, "build-agent-metrics-report:", err)
	return 1
}
