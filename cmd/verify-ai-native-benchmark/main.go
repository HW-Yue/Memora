package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/HW-Yue/Memora/internal/benchmark"
)

func main() {
	reportPath := flag.String("report", "benchmarks/reports/ai-native-release-v0.json", "checked-in F51 benchmark report")
	flag.Parse()
	release, err := benchmark.LoadReleaseBenchmark(*reportPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if release.Gate.Status != "passed" {
		fmt.Fprintf(os.Stderr, "AI-native release gate failed (%d failures)\n", len(release.Gate.Failures))
		for _, failure := range release.Gate.Failures {
			fmt.Fprintln(os.Stderr, "-", failure)
		}
		os.Exit(1)
	}
	fmt.Printf("AI-native release gate passed: %s\n", release.Hash)
}
