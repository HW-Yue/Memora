package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answercorpus"
	"github.com/HW-Yue/Memora/internal/answerevaluation"
)

type commandDependencies struct {
	loadBundle        func(string, string) (answercorpus.Bundle, error)
	loadReports       func(string, string) (answerbenchmark.PublicScorecard, answerbenchmark.PrivateDiagnostics, error)
	newEvaluator      func(answerevaluation.ProcessEvaluatorConfig) (answerevaluation.Evaluator, error)
	runEvaluation     func(context.Context, answercorpus.Bundle, answerbenchmark.PublicScorecard, answerbenchmark.PrivateDiagnostics, answerevaluation.Evaluator) (answerevaluation.Report, error)
	publish           func(string, answerevaluation.Report) error
	lookupEnvironment func(string) (string, bool)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, productionDependencies()))
}

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer, dependencies commandDependencies) int {
	flags := flag.NewFlagSet("run-answer-evaluation", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "absolute public answer manifest JSON path")
	truthPath := flags.String("ground-truth", "", "absolute evaluator-only ground truth JSON path")
	scorecardPath := flags.String("scorecard", "", "absolute F183 public scorecard JSON path")
	diagnosticsPath := flags.String("diagnostics", "", "absolute F183 private diagnostics JSON path")
	evaluatorPath := flags.String("evaluator", "", "absolute evaluator executable path")
	outputPath := flags.String("output", "", "absolute new public evaluation report path")
	apiBaseURL := flags.String("api-base-url", "", "OpenAI-compatible judge API base URL")
	judgeModel := flags.String("judge-model", "", "judge model passed to the evaluator")
	secretName := flags.String("secret-env", "", "parent environment variable holding the judge secret")
	timeout := flags.Duration("timeout", 15*time.Minute, "maximum external evaluator runtime")
	var evaluatorArguments stringList
	var passEnvironment stringList
	flags.Var(&evaluatorArguments, "evaluator-arg", "argument passed to the evaluator; repeatable")
	flags.Var(&passEnvironment, "pass-env", "additional parent environment variable explicitly passed to evaluator; repeatable")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	paths := []string{*manifestPath, *truthPath, *scorecardPath, *diagnosticsPath, *evaluatorPath, *outputPath}
	if flags.NArg() != 0 || !allAbsoluteNormalized(paths) || !validAPIBaseURL(*apiBaseURL) ||
		!validSetting(*judgeModel) || !validEnvironmentName(*secretName) || *timeout <= 0 ||
		reservedEnvironmentName(*secretName) || !validPassedEnvironment(passEnvironment, *secretName) {
		fmt.Fprintln(stderr, "run-answer-evaluation: all artifact/evaluator/output paths must be absolute normalized paths; API URL, judge model, secret env and timeout must be valid")
		return 2
	}
	if _, err := os.Lstat(*outputPath); err == nil {
		fmt.Fprintln(stderr, "run-answer-evaluation:", answerevaluation.ErrReportOutputExists)
		return 1
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stderr, "run-answer-evaluation: cannot inspect output")
		return 1
	}
	if dependencies.lookupEnvironment == nil {
		return commandFailure(stderr, "environment resolver is unavailable")
	}
	secret, available := dependencies.lookupEnvironment(*secretName)
	if !available || secret == "" {
		return commandFailure(stderr, "configured evaluator secret is unavailable")
	}
	environment := []string{
		"MEMORA_EVAL_API_BASE_URL=" + *apiBaseURL,
		"MEMORA_EVAL_MODEL=" + *judgeModel,
		"MEMORA_EVAL_SECRET_ENV=" + *secretName,
		*secretName + "=" + secret,
	}
	for _, name := range passEnvironment {
		value, exists := dependencies.lookupEnvironment(name)
		if !exists {
			return commandFailure(stderr, "an explicitly passed evaluator environment variable is unavailable")
		}
		environment = append(environment, name+"="+value)
	}
	if dependencies.loadBundle == nil || dependencies.loadReports == nil || dependencies.newEvaluator == nil ||
		dependencies.runEvaluation == nil || dependencies.publish == nil {
		return commandFailure(stderr, "command dependencies are unavailable")
	}
	bundle, err := dependencies.loadBundle(*manifestPath, *truthPath)
	if err != nil {
		return commandFailure(stderr, "load answer corpus failed")
	}
	public, private, err := dependencies.loadReports(*scorecardPath, *diagnosticsPath)
	if err != nil {
		return commandFailure(stderr, "load answer benchmark reports failed")
	}
	evaluator, err := dependencies.newEvaluator(answerevaluation.ProcessEvaluatorConfig{
		Executable: *evaluatorPath, Arguments: append([]string(nil), evaluatorArguments...),
		Environment: environment,
	})
	if err != nil {
		return commandFailure(stderr, "configure evaluator failed")
	}
	evaluationContext, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	report, err := dependencies.runEvaluation(evaluationContext, bundle, public, private, evaluator)
	if err != nil {
		if evaluationContext.Err() != nil {
			return commandFailure(stderr, evaluationContext.Err().Error())
		}
		return commandFailure(stderr, "external answer evaluation failed")
	}
	if err := dependencies.publish(*outputPath, report); err != nil {
		return commandFailure(stderr, err.Error())
	}
	fmt.Fprintf(stdout, "Answer evaluation completed: cases=%d scored=%d runner_failed=%d evaluator_failed=%d output=%s report=%s\n",
		report.Counts.Cases, report.Counts.Scored, report.Counts.RunnerFailed,
		report.Counts.EvaluatorFailed, *outputPath, report.Hash)
	return 0
}

func productionDependencies() commandDependencies {
	return commandDependencies{
		loadBundle:  answercorpus.Load,
		loadReports: answerevaluation.LoadReports,
		newEvaluator: func(config answerevaluation.ProcessEvaluatorConfig) (answerevaluation.Evaluator, error) {
			return answerevaluation.NewProcessEvaluator(config)
		},
		runEvaluation: func(
			ctx context.Context,
			bundle answercorpus.Bundle,
			public answerbenchmark.PublicScorecard,
			private answerbenchmark.PrivateDiagnostics,
			evaluator answerevaluation.Evaluator,
		) (answerevaluation.Report, error) {
			runner, err := answerevaluation.NewRunner(evaluator)
			if err != nil {
				return answerevaluation.Report{}, err
			}
			return runner.Run(ctx, bundle, public, private)
		},
		publish:           answerevaluation.PublishReport,
		lookupEnvironment: os.LookupEnv,
	}
}

type stringList []string

func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func (values *stringList) String() string { return strings.Join(*values, ",") }

func allAbsoluteNormalized(paths []string) bool {
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return false
		}
	}
	return true
}

func validAPIBaseURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" &&
		parsed.User == nil && !strings.ContainsRune(value, '\x00')
}

func validSetting(value string) bool {
	return strings.TrimSpace(value) == value && value != "" && len(value) <= 200 && !strings.ContainsRune(value, '\x00')
}

func validPassedEnvironment(values []string, secretName string) bool {
	seen := map[string]bool{secretName: true}
	for _, value := range values {
		if !validEnvironmentName(value) || reservedEnvironmentName(value) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func reservedEnvironmentName(value string) bool {
	switch value {
	case "MEMORA_EVAL_API_BASE_URL", "MEMORA_EVAL_MODEL", "MEMORA_EVAL_SECRET_ENV":
		return true
	default:
		return false
	}
}

func validEnvironmentName(value string) bool {
	if value == "" || !asciiEnvironmentStart(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		if !asciiEnvironmentStart(value[index]) && (value[index] < '0' || value[index] > '9') {
			return false
		}
	}
	return true
}

func asciiEnvironmentStart(value byte) bool {
	return (value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z') || value == '_'
}

func commandFailure(stderr io.Writer, message string) int {
	fmt.Fprintln(stderr, "run-answer-evaluation:", message)
	return 1
}
