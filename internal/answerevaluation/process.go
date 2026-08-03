package answerevaluation

import (
	"context"
	"errors"
)

var ErrProcessEvaluation = errors.New("external evaluator process failed")

type ProcessEvaluatorConfig struct {
	Executable  string
	Arguments   []string
	Environment []string
	TempRoot    string
}

type ProcessEvaluator struct{}

func NewProcessEvaluator(ProcessEvaluatorConfig) (*ProcessEvaluator, error) {
	return nil, ErrProcessEvaluation
}

func (*ProcessEvaluator) Evaluate(context.Context, Input) (Output, error) {
	return Output{}, ErrProcessEvaluation
}
