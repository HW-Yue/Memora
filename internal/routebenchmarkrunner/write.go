package routebenchmarkrunner

import (
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/result"
)

func WriteAtomic(path string, report Report) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return runnerError(result.CodeValidation, "report path must be absolute and normalized")
	}
	encoded, err := Encode(report)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return runnerError(result.CodeInternal, "create report directory: %v", err)
	}
	temporary, err := os.CreateTemp(directory, ".route-benchmark-*.json")
	if err != nil {
		return runnerError(result.CodeInternal, "create report staging file: %v", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() { _ = os.Remove(temporaryPath) }
	defer cleanup()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return runnerError(result.CodeInternal, "set report permissions: %v", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return runnerError(result.CodeInternal, "write report: %v", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return runnerError(result.CodeInternal, "sync report: %v", err)
	}
	if err := temporary.Close(); err != nil {
		return runnerError(result.CodeInternal, "close report: %v", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return runnerError(result.CodeInternal, "publish report: %v", err)
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return runnerError(result.CodeInternal, "open report directory: %v", err)
	}
	defer func() { _ = directoryHandle.Close() }()
	if err := directoryHandle.Sync(); err != nil {
		return runnerError(result.CodeInternal, "sync report directory: %v", err)
	}
	return nil
}
