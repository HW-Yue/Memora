package answerevaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
)

const OutputVersion = "memora.external-evaluator-output/v1"

var ErrInvalidOutput = errors.New("external answer evaluator output is invalid")

type MetricScores struct {
	FactualCorrectness *float64 `json:"factual_correctness,omitempty"`
	Faithfulness       *float64 `json:"faithfulness,omitempty"`
	ContextPrecision   *float64 `json:"context_precision,omitempty"`
	ContextRecall      *float64 `json:"context_recall,omitempty"`
}

type OutputCase struct {
	CaseID    string       `json:"case_id"`
	Status    string       `json:"status"`
	ErrorCode string       `json:"error_code,omitempty"`
	Scores    MetricScores `json:"scores"`
}

type Output struct {
	Version        string       `json:"version"`
	InputSHA256    string       `json:"input_sha256"`
	AdapterID      string       `json:"adapter_id"`
	AdapterVersion string       `json:"adapter_version"`
	JudgeModel     string       `json:"judge_model"`
	Cases          []OutputCase `json:"cases"`
	Hash           string       `json:"hash"`
}

func (output *Output) Seal() error {
	if output == nil || !validOutput(*output, false) {
		return ErrInvalidOutput
	}
	digest, err := outputDigest(*output)
	if err != nil {
		return ErrInvalidOutput
	}
	output.Hash = digest
	return nil
}

func (output Output) Validate() error {
	if !validOutput(output, true) {
		return ErrInvalidOutput
	}
	digest, err := outputDigest(output)
	if err != nil || output.Hash != digest {
		return ErrInvalidOutput
	}
	return nil
}

func validOutput(output Output, sealed bool) bool {
	if output.Version != OutputVersion || !validDigest(output.InputSHA256) ||
		!validText(output.AdapterID, 200) || !validText(output.AdapterVersion, 200) ||
		!validText(output.JudgeModel, 200) || output.Cases == nil || len(output.Cases) == 0 ||
		sealed != validDigest(output.Hash) {
		return false
	}
	previous := ""
	for _, testCase := range output.Cases {
		if !validText(testCase.CaseID, 200) || testCase.CaseID <= previous {
			return false
		}
		switch testCase.Status {
		case "scored":
			if testCase.ErrorCode != "" || !validScores(testCase.Scores, true) {
				return false
			}
		case "runner_failed":
			if testCase.ErrorCode != "" || !validScores(testCase.Scores, false) {
				return false
			}
		case "evaluator_failed":
			if !validErrorCode(testCase.ErrorCode) || !validScores(testCase.Scores, false) {
				return false
			}
		default:
			return false
		}
		previous = testCase.CaseID
	}
	return true
}

func validScores(scores MetricScores, present bool) bool {
	values := []*float64{
		scores.FactualCorrectness, scores.Faithfulness,
		scores.ContextPrecision, scores.ContextRecall,
	}
	for _, value := range values {
		if present != (value != nil) {
			return false
		}
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 1) {
			return false
		}
	}
	return true
}

func validErrorCode(value string) bool {
	if !validText(value, 80) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '_' || character == '-' || character == '.' || character == ':' {
			continue
		}
		return false
	}
	return true
}

func outputDigest(output Output) (string, error) {
	output.Hash = ""
	encoded, err := json.Marshal(output)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
