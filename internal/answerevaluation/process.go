package answerevaluation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var ErrProcessEvaluation = errors.New("external evaluator process failed")

type ProcessEvaluatorConfig struct {
	Executable  string
	Arguments   []string
	Environment []string
	TempRoot    string
}

const maximumEvaluatorOutputBytes = 16 << 20

type ProcessEvaluator struct {
	executable  string
	arguments   []string
	environment []string
	tempRoot    string
}

func NewProcessEvaluator(config ProcessEvaluatorConfig) (*ProcessEvaluator, error) {
	if !filepath.IsAbs(config.Executable) || !validProcessValue(config.Executable, 4096) ||
		(config.TempRoot != "" && !filepath.IsAbs(config.TempRoot)) ||
		!validProcessArguments(config.Arguments) || !validProcessEnvironment(config.Environment) {
		return nil, ErrProcessEvaluation
	}
	info, err := os.Stat(config.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, ErrProcessEvaluation
	}
	if config.TempRoot != "" {
		info, err = os.Stat(config.TempRoot)
		if err != nil || !info.IsDir() {
			return nil, ErrProcessEvaluation
		}
	}
	return &ProcessEvaluator{
		executable: config.Executable, arguments: append([]string(nil), config.Arguments...),
		environment: append(make([]string, 0, len(config.Environment)), config.Environment...),
		tempRoot:    config.TempRoot,
	}, nil
}

func (evaluator *ProcessEvaluator) Evaluate(ctx context.Context, input Input) (Output, error) {
	if evaluator == nil || ctx == nil || input.Validate() != nil {
		return Output{}, ErrProcessEvaluation
	}
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}
	directory, err := os.MkdirTemp(evaluator.tempRoot, "memora-answer-evaluation-")
	if err != nil {
		return Output{}, ErrProcessEvaluation
	}
	defer func() { _ = os.RemoveAll(directory) }()
	if err := os.Chmod(directory, 0o700); err != nil {
		return Output{}, ErrProcessEvaluation
	}
	inputPath, outputPath := filepath.Join(directory, "input.json"), filepath.Join(directory, "output.json")
	encoded, err := json.Marshal(input)
	if err != nil || writePrivateFile(inputPath, encoded) != nil || writePrivateFile(outputPath, nil) != nil {
		return Output{}, ErrProcessEvaluation
	}
	arguments := make([]string, 0, len(evaluator.arguments)+4)
	arguments = append(arguments, evaluator.arguments...)
	arguments = append(arguments, "--input", inputPath, "--output", outputPath)
	command := exec.CommandContext(ctx, evaluator.executable, arguments...)
	command.Env = append(make([]string, 0, len(evaluator.environment)), evaluator.environment...)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return Output{}, ctx.Err()
		}
		return Output{}, ErrProcessEvaluation
	}
	output, err := readStrictOutput(outputPath)
	if err != nil {
		return Output{}, ErrProcessEvaluation
	}
	if output.Hash == "" {
		err = output.Seal()
	} else {
		err = output.Validate()
	}
	if err != nil || output.InputSHA256 != input.Hash || len(output.Cases) != len(input.Cases) {
		return Output{}, ErrProcessEvaluation
	}
	for index := range input.Cases {
		if output.Cases[index].CaseID != input.Cases[index].CaseID ||
			!compatibleStatuses(input.Cases[index].RunnerStatus, output.Cases[index].Status) {
			return Output{}, ErrProcessEvaluation
		}
	}
	return output, nil
}

func writePrivateFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func readStrictOutput(path string) (Output, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximumEvaluatorOutputBytes ||
		info.Mode().Perm()&0o077 != 0 {
		return Output{}, ErrProcessEvaluation
	}
	encoded, err := os.ReadFile(path)
	if err != nil || len(encoded) > maximumEvaluatorOutputBytes {
		return Output{}, ErrProcessEvaluation
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return Output{}, ErrProcessEvaluation
	}
	if _, exists := fields["hash"]; !exists {
		return Output{}, ErrProcessEvaluation
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var output Output
	if err := decoder.Decode(&output); err != nil {
		return Output{}, ErrProcessEvaluation
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Output{}, ErrProcessEvaluation
	}
	return output, nil
}

func validProcessArguments(arguments []string) bool {
	if len(arguments) > 100 {
		return false
	}
	for _, argument := range arguments {
		if !validProcessValue(argument, 1<<20) || argument == "--input" || argument == "--output" {
			return false
		}
	}
	return true
}

func validProcessEnvironment(environment []string) bool {
	seen := make(map[string]bool, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if !found || !validEnvironmentName(name) || !validProcessValue(value, 1<<20) || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

func validEnvironmentName(name string) bool {
	if name == "" || !((name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= 'a' && name[0] <= 'z') || name[0] == '_') {
		return false
	}
	for _, character := range name[1:] {
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validProcessValue(value string, maximum int) bool {
	return len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}
