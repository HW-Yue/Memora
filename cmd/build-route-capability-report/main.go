package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/hostcontract"
	"github.com/HW-Yue/Memora/internal/routebenchmark"
	"github.com/HW-Yue/Memora/internal/routebenchmarkrunner"
	"github.com/HW-Yue/Memora/internal/routecapability"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

type paths []string

func (values *paths) String() string { return fmt.Sprint([]string(*values)) }
func (values *paths) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func run(_ context.Context, arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("build-route-capability-report", flag.ContinueOnError)
	flags.SetOutput(stderr)
	corpusPath := flags.String("corpus", "", "absolute frozen Route Corpus JSON path")
	canonicalDir := flags.String("canonical-skill", "", "absolute Canonical Skill directory")
	pricePath := flags.String("price-card", "", "optional absolute versioned price-card JSON path")
	outputPath := flags.String("output", "", "absolute capability report JSON path")
	var sourcePaths paths
	flags.Var(&sourcePaths, "source", "absolute F125 source report path; repeatable")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !absolute(*corpusPath) || !absolute(*canonicalDir) ||
		!absolute(*outputPath) || len(sourcePaths) == 0 {
		fmt.Fprintln(stderr, "build-route-capability-report: corpus, canonical-skill, source, and output require absolute normalized paths")
		return 2
	}
	for _, path := range sourcePaths {
		if !absolute(path) {
			fmt.Fprintln(stderr, "build-route-capability-report: every source path must be absolute and normalized")
			return 2
		}
	}
	if *pricePath != "" && !absolute(*pricePath) {
		fmt.Fprintln(stderr, "build-route-capability-report: price-card path must be absolute and normalized")
		return 2
	}
	corpus, err := routebenchmark.Load(*corpusPath)
	if err != nil {
		return fail(stderr, err)
	}
	contract, contractDigest, err := hostcontract.Load(filepath.Join(*canonicalDir, "host-contract.json"))
	if err != nil {
		return fail(stderr, err)
	}
	sources := make([]routebenchmarkrunner.Report, 0, len(sourcePaths))
	for _, path := range sourcePaths {
		source, err := routebenchmarkrunner.Load(path)
		if err != nil {
			return fail(stderr, err)
		}
		sources = append(sources, source)
	}
	var card *routecapability.PriceCard
	if *pricePath != "" {
		loaded, err := routecapability.LoadPriceCard(*pricePath)
		if err != nil {
			return fail(stderr, err)
		}
		card = &loaded
	}
	report, err := routecapability.Build(corpus, contract, contractDigest, sources, card)
	if err != nil {
		return fail(stderr, err)
	}
	if err := report.ValidateAgainst(corpus, contract, contractDigest, sources, card); err != nil {
		return fail(stderr, err)
	}
	if err := routecapability.WriteAtomic(*outputPath, report); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "Route capability report: default=%s buckets=%d output=%s hash=%s\n",
		report.Decision.DefaultArm, len(report.Buckets), *outputPath, report.Hash)
	return 0
}

func absolute(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "build-route-capability-report:", err)
	return 1
}
