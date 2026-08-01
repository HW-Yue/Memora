package routebenchmarkrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/result"
)

const (
	maxDriverOutputBytes = 2 << 20
	maxDriverErrorBytes  = 8 << 10
)

type CommandDriver struct {
	executable string
	arguments  []string
}

func NewCommandDriver(executable string, arguments []string) (*CommandDriver, error) {
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return nil, runnerError(result.CodeValidation, "driver executable must be absolute and normalized")
	}
	if containsUnsafeArgument(executable) {
		return nil, runnerError(result.CodeValidation, "driver executable contains forbidden secret or URL material")
	}
	cloned := append([]string(nil), arguments...)
	for _, argument := range cloned {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, 0) || containsUnsafeArgument(argument) {
			return nil, runnerError(result.CodeValidation, "driver argument contains forbidden secret or URL material")
		}
	}
	return &CommandDriver{executable: executable, arguments: cloned}, nil
}

func (driver *CommandDriver) Run(ctx context.Context, request Request) (Observation, error) {
	if driver == nil || driver.executable == "" {
		return Observation{}, runnerError(result.CodeInternal, "command driver is not configured")
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return Observation{}, runnerError(result.CodeInternal, "encode driver request: %v", err)
	}
	timeout := time.Duration(request.Task.Budget.MaxElapsedMillis) * time.Millisecond
	if timeout <= 0 {
		return Observation{}, runnerError(result.CodeValidation, "driver Task timeout is invalid")
	}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(runContext, driver.executable, driver.arguments...)
	command.Stdin = bytes.NewReader(append(encoded, '\n'))
	command.WaitDelay = 2 * time.Second
	stdout, stderr := &boundedWriter{limit: maxDriverOutputBytes}, &boundedWriter{limit: maxDriverErrorBytes}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		if runContext.Err() != nil {
			return Observation{}, runnerError(result.CodeCancelled, "driver did not complete for run %s", request.RunID)
		}
		return Observation{}, runnerError(result.CodeInternal,
			"driver process failed for run %s (stderr_bytes=%d)", request.RunID, stderr.total)
	}
	if stdout.overflow {
		return Observation{}, runnerError(result.CodeOutputTruncated,
			"driver output exceeded %d bytes for run %s", maxDriverOutputBytes, request.RunID)
	}
	var observation Observation
	decoder := json.NewDecoder(bytes.NewReader(stdout.bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&observation); err != nil {
		return Observation{}, runnerError(result.CodeValidation, "decode driver output for run %s", request.RunID)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Observation{}, runnerError(result.CodeValidation, "driver output has trailing JSON for run %s", request.RunID)
	}
	return observation, nil
}

type boundedWriter struct {
	limit    uint64
	total    uint64
	buffer   bytes.Buffer
	overflow bool
}

func (writer *boundedWriter) Write(value []byte) (int, error) {
	writable := uint64(len(value))
	if writer.total < writer.limit {
		remaining := writer.limit - writer.total
		if writable > remaining {
			writable = remaining
		}
		_, _ = writer.buffer.Write(value[:writable])
	}
	writer.total += uint64(len(value))
	writer.overflow = writer.total > writer.limit
	return len(value), nil
}

func (writer *boundedWriter) bytes() []byte { return writer.buffer.Bytes() }

func containsUnsafeArgument(value string) bool {
	lower := strings.ToLower(value)
	if strings.Contains(lower, "://") || strings.Contains(lower, "bearer ") {
		return true
	}
	for _, marker := range []string{"--api-key", "--apikey", "--access-token", "--authorization", "--password", "--secret"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
