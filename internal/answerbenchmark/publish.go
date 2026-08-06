package answerbenchmark

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	PublicScorecardFile    = "scorecard.json"
	PrivateDiagnosticsFile = "diagnostics.json"
)

var (
	ErrInvalidOutput = errors.New("answer benchmark output is invalid")
	ErrOutputExists  = errors.New("answer benchmark output already exists")
)

func PublishReports(outputDirectory string, public PublicScorecard, private PrivateDiagnostics) error {
	if public.Validate() != nil || private.Validate() != nil || private.RunID != public.RunID ||
		private.PublicScorecardSHA256 != public.Hash || !validOutputDirectory(outputDirectory) {
		return ErrInvalidOutput
	}
	if _, err := os.Lstat(outputDirectory); err == nil {
		return ErrOutputExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect destination: %v", ErrInvalidOutput, err)
	}
	publicBytes, err := encodeReport(public)
	if err != nil {
		return fmt.Errorf("%w: encode public scorecard", ErrInvalidOutput)
	}
	privateBytes, err := encodeReport(private)
	if err != nil {
		return fmt.Errorf("%w: encode private diagnostics", ErrInvalidOutput)
	}
	parent := filepath.Dir(outputDirectory)
	parentInfo, err := os.Stat(parent)
	if err != nil || !parentInfo.IsDir() {
		return fmt.Errorf("%w: output parent is unavailable", ErrInvalidOutput)
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(outputDirectory)+".staging-")
	if err != nil {
		return fmt.Errorf("%w: create staging directory: %v", ErrInvalidOutput, err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := os.Chmod(staging, 0o700); err != nil {
		return fmt.Errorf("%w: protect staging directory: %v", ErrInvalidOutput, err)
	}
	if err := writeReport(filepath.Join(staging, PublicScorecardFile), publicBytes, 0o644); err != nil {
		return err
	}
	if err := writeReport(filepath.Join(staging, PrivateDiagnosticsFile), privateBytes, 0o600); err != nil {
		return err
	}
	if err := syncDirectory(staging); err != nil {
		return fmt.Errorf("%w: sync staging directory: %v", ErrInvalidOutput, err)
	}
	if _, err := os.Lstat(outputDirectory); err == nil {
		return ErrOutputExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: recheck destination: %v", ErrInvalidOutput, err)
	}
	if err := renameDirectoryExclusive(staging, outputDirectory); err != nil {
		return err
	}
	published = true
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("%w: sync output parent: %v", ErrInvalidOutput, err)
	}
	return nil
}

func validOutputDirectory(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && strings.TrimSpace(filepath.Base(path)) != "" &&
		filepath.Base(path) != "." && filepath.Base(path) != string(filepath.Separator)
}

func encodeReport(value any) ([]byte, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func writeReport(path string, content []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("%w: create report file: %v", ErrInvalidOutput, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("%w: write report file: %v", ErrInvalidOutput, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("%w: sync report file: %v", ErrInvalidOutput, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: close report file: %v", ErrInvalidOutput, err)
	}
	closed = true
	return nil
}
