package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answercorpus"
	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/instance"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
	"github.com/HW-Yue/Memora/sdk/memora"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("run-answer-benchmark", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "absolute public answer manifest JSON path")
	outputDirectory := flags.String("output-dir", "", "absolute new report directory")
	runID := flags.String("run-id", "", "stable benchmark run identity")
	providerID := flags.String("provider", "kimi", "Provider identity recorded in the scorecard")
	apiBaseURL := flags.String("api-base-url", "https://api.moonshot.cn/v1", "OpenAI-compatible API base URL")
	model := flags.String("model", "moonshot-v1-8k", "Provider model")
	dialect := flags.String("dialect", string(agent.OpenAICompatibleDialectStandard), "OpenAI-compatible wire dialect")
	secretName := flags.String("secret-env", "MOONSHOT_API_KEY", "environment variable holding the Provider secret")
	timeout := flags.Duration("timeout", 2*time.Minute, "timeout for each Provider HTTP request")
	armID := flags.String("arm", answerbenchmark.ArmAtlasLexicalPrefetch, "retrieval arm identity")
	promptID := flags.String("prompt-id", "query-agent-v1", "Query Agent prompt identity")
	codeRevision := flags.String("code-revision", "", "code revision recorded in the scorecard")
	checkpointPath := flags.String("checkpoint", "", "optional absolute checkpoint JSON path for resumable low-concurrency runs")
	maxAttempts := flags.Int("max-attempts", 4, "maximum Provider attempts per question")
	backoff := flags.Duration("backoff", 250*time.Millisecond, "initial retry backoff")
	maxBackoff := flags.Duration("max-backoff", 8*time.Second, "maximum retry backoff")
	maxProviderCalls := flags.Uint64("max-provider-calls", 0, "optional per-question Query Agent Provider-call override; zero keeps manifest")
	maxToolCalls := flags.Uint64("max-tool-calls", 0, "optional per-question Query Agent tool-call override; zero keeps manifest")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !absoluteNormalized(*manifestPath) || !absoluteNormalized(*outputDirectory) ||
		*runID == "" || *providerID == "" || *model == "" || *secretName == "" || *timeout <= 0 ||
		*armID == "" || *promptID == "" || *codeRevision == "" || *dialect != string(agent.OpenAICompatibleDialectStandard) && *dialect != string(agent.OpenAICompatibleDialectDeepSeekV4NonThinking) ||
		*maxAttempts < 1 || *maxAttempts > 8 ||
		*backoff < 0 || *maxBackoff < *backoff || *maxBackoff > 10*time.Minute ||
		(*maxProviderCalls == 0) != (*maxToolCalls == 0) ||
		*maxProviderCalls > 16 || *maxToolCalls > 16 ||
		(*maxProviderCalls != 0 && *maxToolCalls >= *maxProviderCalls) ||
		*checkpointPath != "" && !absoluteNormalized(*checkpointPath) {
		_, _ = fmt.Fprintln(stderr, "run-answer-benchmark: manifest/output-dir must be absolute normalized paths and run/provider/model/secret-env/arm/prompt-id/code-revision must be set")
		return 2
	}
	if _, err := os.Lstat(*outputDirectory); err == nil {
		_, _ = fmt.Fprintln(stderr, "run-answer-benchmark:", answerbenchmark.ErrOutputExists)
		return 1
	} else if !errors.Is(err, os.ErrNotExist) {
		_, _ = fmt.Fprintln(stderr, "run-answer-benchmark: inspect output:", err)
		return 1
	}
	manifest, err := answercorpus.LoadManifest(*manifestPath)
	if err != nil {
		return fail(stderr, err)
	}
	openAIProvider, err := agent.NewOpenAICompatibleProvider(agent.OpenAICompatibleProviderConfig{
		APIBaseURL: *apiBaseURL, SecretName: *secretName, Dialect: agent.OpenAICompatibleDialect(*dialect), Timeout: *timeout,
		MaxRequestBytes: 2 << 20, MaxResponseBytes: 8 << 20,
	}, agent.EnvironmentProviderSecretResolver{})
	if err != nil {
		return fail(stderr, err)
	}
	var provider agent.Provider
	provider, err = agent.NewRetryingProvider(openAIProvider, agent.RetryPolicy{MaxAttempts: *maxAttempts, InitialDelay: *backoff, MaxDelay: *maxBackoff})
	if err != nil {
		return fail(stderr, err)
	}
	public, private, err := runCleanInstance(ctx, manifest, provider, answerbenchmark.RunConfig{
		RunID: *runID, ProviderID: *providerID, Model: *model, ArmID: *armID,
		PromptID: *promptID, CodeRevision: *codeRevision, CheckpointPath: *checkpointPath,
		MaxProviderCalls: *maxProviderCalls, MaxToolCalls: *maxToolCalls,
	})
	if err != nil {
		return fail(stderr, err)
	}
	if err := answerbenchmark.PublishReports(*outputDirectory, public, private); err != nil {
		return fail(stderr, err)
	}
	succeeded := 0
	for _, testCase := range public.Cases {
		if testCase.Status == "succeeded" {
			succeeded++
		}
	}
	_, _ = fmt.Fprintf(stdout, "Answer benchmark completed: cases=%d succeeded=%d failed=%d output=%s scorecard=%s\n",
		len(public.Cases), succeeded, len(public.Cases)-succeeded, *outputDirectory, public.Hash)
	return 0
}

func runCleanInstance(
	ctx context.Context,
	manifest answercorpus.Manifest,
	provider agent.Provider,
	config answerbenchmark.RunConfig,
) (PublicScorecard, PrivateDiagnostics, error) {
	dataDirectory, err := os.MkdirTemp("", "memora-answer-benchmark-")
	if err != nil {
		return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("create clean benchmark Instance: %w", err)
	}
	defer func() { _ = os.RemoveAll(dataDirectory) }()
	if _, err := instance.Initialize(ctx, dataDirectory, instance.Options{}); err != nil {
		return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("initialize clean benchmark Instance: %w", err)
	}
	daemonContext, cancelDaemon := context.WithCancel(ctx)
	ready := make(chan daemon.State, 1)
	done := make(chan error, 1)
	go func() { done <- daemon.Run(daemonContext, dataDirectory, ready) }()
	select {
	case <-ready:
	case err := <-done:
		cancelDaemon()
		return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("start clean benchmark daemon: %w", err)
	case <-ctx.Done():
		cancelDaemon()
		<-done
		return PublicScorecard{}, PrivateDiagnostics{}, ctx.Err()
	}
	client, err := memora.Dial(ctx, memora.DialOptions{DataDir: dataDirectory})
	if err != nil {
		cancelDaemon()
		<-done
		return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("connect clean benchmark daemon: %w", err)
	}
	runner, runnerErr := answerbenchmark.NewRunner(answerbenchmark.RunnerDependencies{
		MSQL: sdkExecutor{client: client}, Provider: provider, Clock: systemClock{},
	})
	var public PublicScorecard
	var private PrivateDiagnostics
	if runnerErr == nil {
		public, private, runnerErr = runner.Run(ctx, manifest, config)
	}
	clientErr := client.Close()
	cancelDaemon()
	daemonErr := <-done
	if runnerErr != nil {
		return PublicScorecard{}, PrivateDiagnostics{}, runnerErr
	}
	if clientErr != nil {
		return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("close benchmark SDK: %w", clientErr)
	}
	if daemonErr != nil && !errors.Is(daemonErr, context.Canceled) {
		return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("stop clean benchmark daemon: %w", daemonErr)
	}
	return public, private, nil
}

type PublicScorecard = answerbenchmark.PublicScorecard
type PrivateDiagnostics = answerbenchmark.PrivateDiagnostics

type sdkExecutor struct{ client *memora.Client }

func (executor sdkExecutor) ExecuteMSQL(ctx context.Context, request protocolmsql.Request) (protocolmsql.Envelope, error) {
	return executor.client.Execute(ctx, request)
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

func absoluteNormalized(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func fail(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintln(stderr, "run-answer-benchmark:", err)
	return 1
}
