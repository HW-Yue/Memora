package answerevaluation

import (
	"errors"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
)

var ErrInvalidArtifacts = errors.New("answer evaluation artifacts are invalid")

func LoadReports(string, string) (answerbenchmark.PublicScorecard, answerbenchmark.PrivateDiagnostics, error) {
	return answerbenchmark.PublicScorecard{}, answerbenchmark.PrivateDiagnostics{}, ErrInvalidArtifacts
}
