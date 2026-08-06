package answerevaluation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrInvalidReportOutput = errors.New("answer evaluation report output is invalid")
	ErrReportOutputExists  = errors.New("answer evaluation report output already exists")
)

func PublishReport(outputPath string, report Report) error {
	if report.Validate() != nil || !validReportPath(outputPath) {
		return ErrInvalidReportOutput
	}
	if _, err := os.Lstat(outputPath); err == nil {
		return ErrReportOutputExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect destination", ErrInvalidReportOutput)
	}
	parent := filepath.Dir(outputPath)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: output parent is unavailable", ErrInvalidReportOutput)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: encode report", ErrInvalidReportOutput)
	}
	encoded = append(encoded, '\n')
	staging, err := os.CreateTemp(parent, "."+filepath.Base(outputPath)+".staging-")
	if err != nil {
		return fmt.Errorf("%w: create staging file", ErrInvalidReportOutput)
	}
	stagingPath := staging.Name()
	closed := false
	defer func() {
		if !closed {
			_ = staging.Close()
		}
		_ = os.Remove(stagingPath)
	}()
	if err := staging.Chmod(0o644); err != nil {
		return fmt.Errorf("%w: protect staging file", ErrInvalidReportOutput)
	}
	if _, err := staging.Write(encoded); err != nil {
		return fmt.Errorf("%w: write staging file", ErrInvalidReportOutput)
	}
	if err := staging.Sync(); err != nil {
		return fmt.Errorf("%w: sync staging file", ErrInvalidReportOutput)
	}
	if err := staging.Close(); err != nil {
		return fmt.Errorf("%w: close staging file", ErrInvalidReportOutput)
	}
	closed = true
	if err := publishFileExclusive(stagingPath, outputPath); err != nil {
		return err
	}
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("%w: sync output parent", ErrInvalidReportOutput)
	}
	return nil
}

func validReportPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path &&
		strings.TrimSpace(filepath.Base(path)) != "" && filepath.Base(path) != "." &&
		filepath.Base(path) != string(filepath.Separator)
}
