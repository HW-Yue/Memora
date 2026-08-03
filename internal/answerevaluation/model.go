// Package answerevaluation owns the evaluator-only bridge between F183
// benchmark artifacts, hidden F182 ground truth, and external scoring tools.
package answerevaluation

import (
	"errors"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answercorpus"
)

const InputVersion = "memora.external-evaluation-input/v1"

var ErrInvalidInput = errors.New("external answer evaluation input is invalid")

type Case struct {
	CaseID            string   `json:"case_id"`
	RunnerStatus      string   `json:"runner_status"`
	UserInput         string   `json:"user_input"`
	Response          string   `json:"response,omitempty"`
	Reference         string   `json:"reference"`
	RetrievedContexts []string `json:"retrieved_contexts"`
}

type Input struct {
	Version                  string `json:"version"`
	RunID                    string `json:"run_id"`
	CorpusID                 string `json:"corpus_id"`
	CorpusRevision           uint64 `json:"corpus_revision"`
	SnapshotSHA256           string `json:"snapshot_sha256"`
	PublicScorecardSHA256    string `json:"public_scorecard_sha256"`
	PrivateDiagnosticsSHA256 string `json:"private_diagnostics_sha256"`
	GroundTruthSHA256        string `json:"ground_truth_sha256"`
	Cases                    []Case `json:"cases"`
	Hash                     string `json:"hash"`
}

func (input Input) Validate() error { return ErrInvalidInput }

func BuildInput(
	answercorpus.Bundle,
	answerbenchmark.PublicScorecard,
	answerbenchmark.PrivateDiagnostics,
) (Input, error) {
	return Input{}, ErrInvalidInput
}
