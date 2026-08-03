package answerrelease

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrInvalidOutput = errors.New("query release output is invalid")
	ErrOutputExists  = errors.New("query release output already exists")
)

func PublishReport(outputPath string, report Report) error {
	if report.Validate() != nil || !validOutputPath(outputPath) {
		return ErrInvalidOutput
	}
	if _, err := os.Lstat(outputPath); err == nil {
		return ErrOutputExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect destination", ErrInvalidOutput)
	}
	parent := filepath.Dir(outputPath)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: output parent is unavailable", ErrInvalidOutput)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: encode report", ErrInvalidOutput)
	}
	encoded = append(encoded, '\n')
	staging, err := os.CreateTemp(parent, "."+filepath.Base(outputPath)+".staging-")
	if err != nil {
		return fmt.Errorf("%w: create staging file", ErrInvalidOutput)
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
		return fmt.Errorf("%w: protect staging file", ErrInvalidOutput)
	}
	if _, err := staging.Write(encoded); err != nil {
		return fmt.Errorf("%w: write staging file", ErrInvalidOutput)
	}
	if err := staging.Sync(); err != nil {
		return fmt.Errorf("%w: sync staging file", ErrInvalidOutput)
	}
	if err := staging.Close(); err != nil {
		return fmt.Errorf("%w: close staging file", ErrInvalidOutput)
	}
	closed = true
	if err := os.Link(stagingPath, outputPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrOutputExists
		}
		return fmt.Errorf("%w: publish report", ErrInvalidOutput)
	}
	directory, err := os.Open(parent)
	if err != nil {
		return fmt.Errorf("%w: open output parent", ErrInvalidOutput)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return fmt.Errorf("%w: sync output parent", ErrInvalidOutput)
	}
	return nil
}

func validOutputPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path &&
		strings.TrimSpace(filepath.Base(path)) != "" && filepath.Base(path) != "." &&
		filepath.Base(path) != string(filepath.Separator)
}
