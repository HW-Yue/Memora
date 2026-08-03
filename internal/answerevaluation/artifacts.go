package answerevaluation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
)

var ErrInvalidArtifacts = errors.New("answer evaluation artifacts are invalid")

const maximumArtifactBytes = 64 << 20

func LoadReports(
	publicPath string,
	privatePath string,
) (answerbenchmark.PublicScorecard, answerbenchmark.PrivateDiagnostics, error) {
	var public answerbenchmark.PublicScorecard
	if err := decodeStrictArtifact(publicPath, false, &public); err != nil {
		return answerbenchmark.PublicScorecard{}, answerbenchmark.PrivateDiagnostics{}, ErrInvalidArtifacts
	}
	var private answerbenchmark.PrivateDiagnostics
	if err := decodeStrictArtifact(privatePath, true, &private); err != nil {
		return answerbenchmark.PublicScorecard{}, answerbenchmark.PrivateDiagnostics{}, ErrInvalidArtifacts
	}
	if public.Validate() != nil || private.Validate() != nil || public.RunID != private.RunID ||
		private.PublicScorecardSHA256 != public.Hash {
		return answerbenchmark.PublicScorecard{}, answerbenchmark.PrivateDiagnostics{}, ErrInvalidArtifacts
	}
	return public, private, nil
}

func decodeStrictArtifact(path string, private bool, target any) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrInvalidArtifacts
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumArtifactBytes ||
		(private && info.Mode().Perm()&0o077 != 0) {
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
