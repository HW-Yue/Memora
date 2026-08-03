package answerevaluation

import "errors"

const OutputVersion = "memora.external-evaluator-output/v1"

var ErrInvalidOutput = errors.New("external answer evaluator output is invalid")

type MetricScores struct {
	FactualCorrectness *float64 `json:"factual_correctness,omitempty"`
	Faithfulness       *float64 `json:"faithfulness,omitempty"`
	ContextPrecision   *float64 `json:"context_precision,omitempty"`
	ContextRecall      *float64 `json:"context_recall,omitempty"`
}

type OutputCase struct {
	CaseID    string       `json:"case_id"`
	Status    string       `json:"status"`
	ErrorCode string       `json:"error_code,omitempty"`
	Scores    MetricScores `json:"scores"`
}

type Output struct {
	Version        string       `json:"version"`
	InputSHA256    string       `json:"input_sha256"`
	AdapterID      string       `json:"adapter_id"`
	AdapterVersion string       `json:"adapter_version"`
	JudgeModel     string       `json:"judge_model"`
	Cases          []OutputCase `json:"cases"`
	Hash           string       `json:"hash"`
}

func (output *Output) Seal() error    { return ErrInvalidOutput }
func (output Output) Validate() error { return ErrInvalidOutput }
