package answerevaluation

import (
	"context"
	"errors"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answercorpus"
)

var ErrEvaluation = errors.New("external answer evaluation failed")

type Evaluator interface {
	Evaluate(context.Context, Input) (Output, error)
}

type Runner struct{ evaluator Evaluator }

func NewRunner(Evaluator) (*Runner, error) { return nil, ErrEvaluation }

func (runner *Runner) Run(
	context.Context,
	answercorpus.Bundle,
	answerbenchmark.PublicScorecard,
	answerbenchmark.PrivateDiagnostics,
) (Report, error) {
	return Report{}, ErrEvaluation
}
