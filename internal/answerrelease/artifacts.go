package answerrelease

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answerevaluation"
)

var ErrInvalidArtifacts = errors.New("query release artifacts are invalid")

const maximumArtifactBytes = 64 << 20

func LoadEvidence(scorecardPaths, evaluationPaths []string) ([]Evidence, error) {
	if len(scorecardPaths) != len(requiredArms) || len(evaluationPaths) != len(requiredArms) {
		return nil, ErrInvalidArtifacts
	}
	scorecards := make([]answerbenchmark.PublicScorecard, len(scorecardPaths))
	byHash := make(map[string]int, len(scorecardPaths))
	for index, path := range scorecardPaths {
		if err := decodeStrictArtifact(path, &scorecards[index]); err != nil || scorecards[index].Validate() != nil ||
			byHash[scorecards[index].Hash] != 0 {
			return nil, ErrInvalidArtifacts
		}
		byHash[scorecards[index].Hash] = index + 1
	}
	evidence := make([]Evidence, len(scorecards))
	for index, scorecard := range scorecards {
		evidence[index].Scorecard = scorecard
	}
	used := make(map[string]bool, len(evaluationPaths))
	for _, path := range evaluationPaths {
		var evaluation answerevaluation.Report
		if err := decodeStrictArtifact(path, &evaluation); err != nil || evaluation.Validate() != nil ||
			used[evaluation.PublicScorecardSHA256] {
			return nil, ErrInvalidArtifacts
		}
		position, found := byHash[evaluation.PublicScorecardSHA256]
		if !found {
			return nil, ErrInvalidArtifacts
		}
		used[evaluation.PublicScorecardSHA256] = true
		evidence[position-1].Evaluation = evaluation
	}
	if len(used) != len(scorecards) {
		return nil, ErrInvalidArtifacts
	}
	if _, err := Build(evidence); err != nil {
		return nil, ErrInvalidArtifacts
	}
	return evidence, nil
}

func LoadReport(path string) (Report, error) {
	var report Report
	if err := decodeStrictArtifact(path, &report); err != nil || report.Validate() != nil {
		return Report{}, ErrInvalidArtifacts
	}
	return report, nil
}

func decodeStrictArtifact(path string, target any) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrInvalidArtifacts
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumArtifactBytes {
		return ErrInvalidArtifacts
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return ErrInvalidArtifacts
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalidArtifacts
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidArtifacts
	}
	return nil
}
