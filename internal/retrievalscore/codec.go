package retrievalscore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const maxInputBytes = 128 << 20

func LoadSuite(path string) (Suite, error) {
	var suite Suite
	if err := loadStrict(path, &suite); err != nil {
		return Suite{}, err
	}
	if err := suite.Validate(); err != nil {
		return Suite{}, err
	}
	return suite, nil
}

func LoadRun(path string) (Run, error) {
	var run Run
	if err := loadStrict(path, &run); err != nil {
		return Run{}, err
	}
	if err := run.Validate(); err != nil {
		return Run{}, err
	}
	return run, nil
}

func EncodeReport(report Report) ([]byte, error) {
	if err := report.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func loadStrict(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maxInputBytes+1))
	if err != nil {
		return err
	}
	if len(encoded) == 0 || len(encoded) > maxInputBytes {
		return errors.New("retrieval evaluation input size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode retrieval evaluation input: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("retrieval evaluation input has trailing content")
	}
	return nil
}
