package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/hostcontract"
	"github.com/HW-Yue/Memora/internal/storygate"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("build-ai-story-gate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	canonicalDir := flags.String("canonical-skill", "", "absolute Canonical Skill directory")
	codexPath := flags.String("codex-runtime", "", "absolute Codex mechanical report JSON")
	claudePath := flags.String("claude-runtime", "", "absolute Claude mechanical report JSON")
	evidencePath := flags.String("evidence", "", "absolute real AI journey evidence JSON")
	outputPath := flags.String("output", "", "absolute AI Story Gate report JSON")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !absolute(*canonicalDir) || !absolute(*codexPath) ||
		!absolute(*claudePath) || !absolute(*evidencePath) || !absolute(*outputPath) {
		_, _ = fmt.Fprintln(stderr, "build-ai-story-gate: all five artifact paths must be absolute and normalized")
		return 2
	}
	contract, contractDigest, err := hostcontract.Load(filepath.Join(*canonicalDir, "host-contract.json"))
	if err != nil {
		return fail(stderr, err)
	}
	suite, err := storygate.GenerateAIJourneySuite()
	if err != nil {
		return fail(stderr, err)
	}
	codex, err := storygate.LoadRuntimeReport(*codexPath)
	if err != nil {
		return fail(stderr, err)
	}
	claude, err := storygate.LoadRuntimeReport(*claudePath)
	if err != nil {
		return fail(stderr, err)
	}
	evidence, err := storygate.LoadAIEvidenceSet(*evidencePath)
	if err != nil {
		return fail(stderr, err)
	}
	if evidence.SuiteDigest != suite.Hash || evidence.ContractDigest != contractDigest {
		return fail(stderr, fmt.Errorf("evidence Suite or contract digest differs"))
	}
	report, err := storygate.BuildAIReport(contract, contractDigest, suite, codex, claude, evidence.Journeys)
	if err != nil {
		return fail(stderr, err)
	}
	if err := storygate.WriteAIReportAtomic(*outputPath, report); err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprintf(stdout, "AI Story Gate passed: journeys=%d output=%s hash=%s\n",
		len(report.Journeys), *outputPath, report.Hash)
	return 0
}

func absolute(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func fail(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintln(stderr, "build-ai-story-gate:", err)
	return 1
}
