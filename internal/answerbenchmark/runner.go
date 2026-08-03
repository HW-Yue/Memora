package answerbenchmark

import (
	"context"
	"errors"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/answercorpus"
)

var (
	ErrInvalidRunner = errors.New("answer benchmark Runner is invalid")
	ErrRunBenchmark  = errors.New("answer benchmark run failed")
)

type RunnerDependencies struct {
	MSQL     agent.MSQLExecutor
	Provider agent.Provider
	Clock    agent.QueryClock
}

type RunConfig struct {
	RunID        string
	ProviderID   string
	Model        string
	ArmID        string
	PromptID     string
	CodeRevision string
}

type Runner struct{}

func NewRunner(RunnerDependencies) (*Runner, error) { return nil, ErrInvalidRunner }

func (runner *Runner) Run(
	context.Context,
	answercorpus.Manifest,
	RunConfig,
) (PublicScorecard, PrivateDiagnostics, error) {
	return PublicScorecard{}, PrivateDiagnostics{}, ErrRunBenchmark
}
