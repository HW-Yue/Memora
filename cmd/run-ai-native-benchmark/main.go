package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/HW-Yue/Memora/internal/benchmark"
)

func main() {
	var (
		suitePath  = flag.String("suite", "benchmarks/ai-native-v1.json", "F42 benchmark suite")
		corpusPath = flag.String("corpus", "benchmarks/ai-native-release-v0.json", "F51 release corpus")
		outputPath = flag.String("output", "benchmarks/reports/ai-native-release-v0.json", "auditable report output")
		baseURL    = flag.String("base-url", os.Getenv("MEMORA_BENCHMARK_OPENAI_BASE_URL"), "OpenAI-compatible HTTPS base URL")
		apiKey     = flag.String("api-key", os.Getenv("MEMORA_BENCHMARK_OPENAI_API_KEY"), "OpenAI-compatible API key (prefer environment)")
		model      = flag.String("model", envOr("MEMORA_BENCHMARK_OPENAI_MODEL", "moonshot-v1-8k"), "model ID")
		provider   = flag.String("provider", envOr("MEMORA_BENCHMARK_PROVIDER", "openai-compatible"), "non-secret provider label")
	)
	flag.Parse()
	suite, err := benchmark.Load(*suitePath)
	exitOnError(err)
	corpus, err := benchmark.LoadReleaseCorpus(*corpusPath)
	exitOnError(err)
	modeler, err := benchmark.NewOpenAIModeler(benchmark.OpenAIModelerConfig{
		BaseURL: *baseURL, APIKey: *apiKey, Model: *model, Provider: *provider, Timeout: 4 * time.Minute,
	})
	exitOnError(err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	release, err := benchmark.RunReleaseBenchmark(ctx, suite, corpus, modeler)
	exitOnError(err)
	encoded, err := json.MarshalIndent(release, "", "  ")
	exitOnError(err)
	encoded = append(encoded, '\n')
	exitOnError(os.MkdirAll(filepath.Dir(*outputPath), 0o755))
	temporary, err := os.CreateTemp(filepath.Dir(*outputPath), ".ai-native-release-*.json")
	exitOnError(err)
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	exitOnError(temporary.Chmod(0o600))
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		exitOnError(err)
	}
	exitOnError(temporary.Sync())
	exitOnError(temporary.Close())
	exitOnError(os.Rename(temporaryPath, *outputPath))
	fmt.Printf("AI-native release gate: %s; report=%s; hash=%s\n", release.Gate.Status, *outputPath, release.Hash)
	for _, failure := range release.Gate.Failures {
		fmt.Printf("- %s\n", failure)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func exitOnError(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
