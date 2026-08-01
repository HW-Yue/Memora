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
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("run-route-benchmark", flag.ContinueOnError)
	flags.SetOutput(stderr)
	corpusPath := flags.String("corpus", "", "absolute frozen Route Corpus JSON path")
	canonicalDir := flags.String("canonical-skill", "", "absolute Canonical Skill directory")
	driversPath := flags.String("drivers", "", "absolute host driver config JSON path")
	outputPath := flags.String("output", "", "absolute new or replaceable report JSON path")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !absoluteNormalized(*corpusPath) || !absoluteNormalized(*canonicalDir) ||
		!absoluteNormalized(*driversPath) || !absoluteNormalized(*outputPath) {
		fmt.Fprintln(stderr, "run-route-benchmark: corpus, canonical-skill, drivers, and output must be absolute normalized paths")
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
	skillDigest, protocolDigest, err := routebenchmarkrunner.CanonicalDigests(*canonicalDir)
	if err != nil {
		return fail(stderr, err)
	}
	config, err := routebenchmarkrunner.LoadDriverConfig(*driversPath, contract)
	if err != nil {
		return fail(stderr, err)
	}
	profiles, err := config.BuildProfiles()
	if err != nil {
		return fail(stderr, err)
	}
	runner, err := routebenchmarkrunner.New(routebenchmarkrunner.Options{
		Contract: contract, ContractDigest: contractDigest,
		SkillDigest: skillDigest, ProtocolDigest: protocolDigest, Profiles: profiles,
	})
	if err != nil {
		return fail(stderr, err)
	}
	report, err := runner.Run(ctx, corpus)
	if err != nil {
		return fail(stderr, err)
	}
	if err := report.ValidateAgainst(corpus, contract, contractDigest); err != nil {
		return fail(stderr, err)
	}
	if err := routebenchmarkrunner.WriteAtomic(*outputPath, report); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "Route benchmark completed: runs=%d failures=%d report=%s hash=%s\n",
		report.Counts.Runs, len(report.Failures), *outputPath, report.Hash)
	return 0
}

func absoluteNormalized(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "run-route-benchmark:", err)
	return 1
}
