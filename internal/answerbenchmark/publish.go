package answerbenchmark

import "errors"

const (
	PublicScorecardFile    = "scorecard.json"
	PrivateDiagnosticsFile = "diagnostics.json"
)

var (
	ErrInvalidOutput = errors.New("answer benchmark output is invalid")
	ErrOutputExists  = errors.New("answer benchmark output already exists")
)

func PublishReports(string, PublicScorecard, PrivateDiagnostics) error { return ErrInvalidOutput }
