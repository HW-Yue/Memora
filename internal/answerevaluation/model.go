// Package answerevaluation owns the evaluator-only bridge between F183
// benchmark artifacts, hidden F182 ground truth, and external scoring tools.
package answerevaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answercorpus"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

const InputVersion = "memora.external-evaluation-input/v1"

var ErrInvalidInput = errors.New("external answer evaluation input is invalid")

type Case struct {
	CaseID            string   `json:"case_id"`
	RunnerStatus      string   `json:"runner_status"`
	UserInput         string   `json:"user_input"`
	Response          string   `json:"response,omitempty"`
	Reference         string   `json:"reference"`
	RetrievedContexts []string `json:"retrieved_contexts"`
}

type Input struct {
	Version                  string `json:"version"`
	RunID                    string `json:"run_id"`
	CorpusID                 string `json:"corpus_id"`
	CorpusRevision           uint64 `json:"corpus_revision"`
	SnapshotSHA256           string `json:"snapshot_sha256"`
	PublicScorecardSHA256    string `json:"public_scorecard_sha256"`
	PrivateDiagnosticsSHA256 string `json:"private_diagnostics_sha256"`
	GroundTruthSHA256        string `json:"ground_truth_sha256"`
	Cases                    []Case `json:"cases"`
	Hash                     string `json:"hash"`
}

func (input *Input) Seal() error {
	if input == nil || !validInput(*input, false) {
		return ErrInvalidInput
	}
	digest, err := inputDigest(*input)
	if err != nil {
		return ErrInvalidInput
	}
	input.Hash = digest
	return nil
}

func (input Input) Validate() error {
	if !validInput(input, true) {
		return ErrInvalidInput
	}
	digest, err := inputDigest(input)
	if err != nil || input.Hash != digest {
		return ErrInvalidInput
	}
	return nil
}

func BuildInput(
	bundle answercorpus.Bundle,
	public answerbenchmark.PublicScorecard,
	private answerbenchmark.PrivateDiagnostics,
) (Input, error) {
	if bundle.Validate() != nil || public.Validate() != nil || private.Validate() != nil ||
		public.RunID != private.RunID || private.PublicScorecardSHA256 != public.Hash ||
		public.CorpusID != bundle.Manifest.CorpusID || public.CorpusRevision != bundle.Manifest.Revision ||
		public.SnapshotID != bundle.Manifest.Fixture.SnapshotID ||
		public.SnapshotSHA256 != bundle.Manifest.Fixture.SnapshotSHA256 ||
		private.Materialization.ManifestSHA256 != bundle.Manifest.Hash ||
		private.Materialization.FixtureSnapshotSHA256 != bundle.Manifest.Fixture.SnapshotSHA256 {
		return Input{}, ErrInvalidInput
	}
	tasks, err := bundle.Manifest.BlindTasks()
	if err != nil || len(public.Cases) != len(tasks) || len(private.Cases) != len(tasks) ||
		len(bundle.GroundTruth.Cases) != len(tasks) {
		return Input{}, ErrInvalidInput
	}
	input := Input{
		Version: InputVersion, RunID: public.RunID, CorpusID: public.CorpusID,
		CorpusRevision: public.CorpusRevision, SnapshotSHA256: public.SnapshotSHA256,
		PublicScorecardSHA256: public.Hash, PrivateDiagnosticsSHA256: private.Hash,
		GroundTruthSHA256: bundle.GroundTruth.Hash, Cases: make([]Case, 0, len(tasks)),
	}
	for index, task := range tasks {
		publicCase, privateCase, truth := public.Cases[index], private.Cases[index], bundle.GroundTruth.Cases[index]
		if publicCase.CaseID != task.CaseID || publicCase.Question != task.Question ||
			!reflect.DeepEqual(privateCase.Task, task) || truth.CaseID != task.CaseID ||
			(publicCase.Status == "succeeded") != (privateCase.Error == "") {
			return Input{}, ErrInvalidInput
		}
		value := Case{
			CaseID: task.CaseID, RunnerStatus: publicCase.Status, UserInput: task.Question,
			Reference: truth.ReferenceAnswer, RetrievedContexts: []string{},
		}
		if publicCase.Status == "succeeded" {
			value.Response = publicCase.Answer
			value.RetrievedContexts, err = canonicalContexts(privateCase.Evidence)
			if err != nil {
				return Input{}, err
			}
		}
		input.Cases = append(input.Cases, value)
	}
	if err := input.Seal(); err != nil {
		return Input{}, err
	}
	return input, nil
}

func canonicalContexts(evidence []agent.SelectEvidence) ([]string, error) {
	contexts := []string{}
	for _, item := range evidence {
		statement := item.Result
		if statement.Statement != "SELECT" || statement.Status != protocolmsql.Status("succeeded") ||
			statement.Error != nil || statement.Columns == nil || statement.Rows == nil {
			return nil, ErrInvalidInput
		}
		seen := make(map[string]bool, len(statement.Columns))
		for _, column := range statement.Columns {
			name := strings.ToLower(column.Name)
			if name == "" || seen[name] {
				return nil, ErrInvalidInput
			}
			seen[name] = true
		}
		for _, row := range statement.Rows {
			encoded, err := canonicalContextRow(statement.Columns, row)
			if err != nil {
				return nil, err
			}
			contexts = append(contexts, encoded)
		}
	}
	return contexts, nil
}

func canonicalContextRow(columns []protocolmsql.Column, row protocolmsql.Row) (string, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	written := 0
	for _, column := range columns {
		if systemContextColumn(column.Name) {
			continue
		}
		value, exists := row[column.Name]
		if !exists {
			return "", ErrInvalidInput
		}
		nameJSON, _ := json.Marshal(column.Name)
		valueJSON, err := json.Marshal(value)
		if err != nil {
			return "", ErrInvalidInput
		}
		if written > 0 {
			buffer.WriteByte(',')
		}
		buffer.Write(nameJSON)
		buffer.WriteByte(':')
		buffer.Write(valueJSON)
		written++
	}
	buffer.WriteByte('}')
	return buffer.String(), nil
}

func systemContextColumn(value string) bool {
	switch strings.ToLower(value) {
	case "row_id", "revision", "commit_sequence", "row_state", "schema_version":
		return true
	default:
		return false
	}
}

func validInput(input Input, sealed bool) bool {
	if input.Version != InputVersion || !validText(input.RunID, 200) || !validText(input.CorpusID, 200) ||
		input.CorpusRevision == 0 || !validDigest(input.SnapshotSHA256) ||
		!validDigest(input.PublicScorecardSHA256) || !validDigest(input.PrivateDiagnosticsSHA256) ||
		!validDigest(input.GroundTruthSHA256) || len(input.Cases) == 0 || input.Cases == nil ||
		sealed != validDigest(input.Hash) {
		return false
	}
	previous := ""
	for _, testCase := range input.Cases {
		if testCase.CaseID <= previous || !validText(testCase.CaseID, 200) ||
			!validText(testCase.UserInput, 2048) || !validText(testCase.Reference, 1<<20) ||
			testCase.RetrievedContexts == nil {
			return false
		}
		switch testCase.RunnerStatus {
		case "succeeded":
			if !validText(testCase.Response, 1<<20) {
				return false
			}
		case "failed":
			if testCase.Response != "" || len(testCase.RetrievedContexts) != 0 {
				return false
			}
		default:
			return false
		}
		for _, context := range testCase.RetrievedContexts {
			if !validText(context, 1<<20) || !validJSONObject(context) {
				return false
			}
		}
		previous = testCase.CaseID
	}
	return true
}

func validJSONObject(value string) bool {
	var object map[string]any
	return json.Unmarshal([]byte(value), &object) == nil && object != nil
}

func inputDigest(input Input) (string, error) {
	input.Hash = ""
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size
}

func validText(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && len(value) <= maximum &&
		!strings.ContainsRune(value, '\x00')
}
