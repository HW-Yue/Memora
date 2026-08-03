package main

import (
	"context"
	"io"

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

func main() {}

func run(context.Context, []string, io.Writer, io.Writer, commandDependencies) int { return 1 }
