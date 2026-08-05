package answerbenchmark

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/HW-Yue/Memora/internal/answercorpus"
)

const CheckpointVersion = "memora.answer-benchmark-checkpoint/v1"

var ErrInvalidCheckpoint = errors.New("answer benchmark checkpoint is invalid")

type Checkpoint struct {
	Version  string           `json:"version"`
	Identity string           `json:"identity"`
	Cases    []CheckpointCase `json:"cases"`
	Hash     string           `json:"hash"`
}

type CheckpointCase struct {
	Public  PublicCase  `json:"public"`
	Private PrivateCase `json:"private"`
}

func CheckpointIdentity(manifest answercorpus.Manifest, config RunConfig) string {
	value := struct {
		CorpusID       string `json:"corpus_id"`
		CorpusRevision uint64 `json:"corpus_revision"`
		SnapshotID     string `json:"snapshot_id"`
		SnapshotSHA256 string `json:"snapshot_sha256"`
		RunID          string `json:"run_id"`
		ProviderID     string `json:"provider_id"`
		Model          string `json:"model"`
		ArmID          string `json:"arm_id"`
		PromptID       string `json:"prompt_id"`
		CodeRevision   string `json:"code_revision"`
	}{manifest.CorpusID, manifest.Revision, manifest.Fixture.SnapshotID, manifest.Fixture.SnapshotSHA256,
		config.RunID, config.ProviderID, config.Model, config.ArmID, config.PromptID, config.CodeRevision}
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (checkpoint *Checkpoint) Seal() error {
	if checkpoint == nil || checkpoint.Version != CheckpointVersion || !validCheckpointText(checkpoint.Identity) || len(checkpoint.Cases) == 0 {
		return ErrInvalidCheckpoint
	}
	checkpoint.Hash = ""
	if err := validateCheckpointCases(checkpoint.Cases); err != nil {
		return err
	}
	digest, err := checkpointDigest(*checkpoint)
	if err != nil {
		return ErrInvalidCheckpoint
	}
	checkpoint.Hash = digest
	return nil
}

func (checkpoint Checkpoint) Validate() error {
	if checkpoint.Version != CheckpointVersion || !validCheckpointDigest(checkpoint.Hash) ||
		!validCheckpointText(checkpoint.Identity) || len(checkpoint.Cases) == 0 {
		return ErrInvalidCheckpoint
	}
	if err := validateCheckpointCases(checkpoint.Cases); err != nil {
		return err
	}
	want := checkpoint.Hash
	checkpoint.Hash = ""
	digest, err := checkpointDigest(checkpoint)
	if err != nil || want != digest {
		return ErrInvalidCheckpoint
	}
	return nil
}

func validateCheckpointCases(cases []CheckpointCase) error {
	seen := make(map[string]struct{}, len(cases))
	for _, value := range cases {
		if value.Public.CaseID == "" || value.Private.Task.CaseID != value.Public.CaseID ||
			value.Public.Question != value.Private.Task.Question || value.Private.Trace.Validate() != nil {
			return ErrInvalidCheckpoint
		}
		if _, duplicate := seen[value.Public.CaseID]; duplicate {
			return ErrInvalidCheckpoint
		}
		seen[value.Public.CaseID] = struct{}{}
		switch value.Public.Status {
		case "succeeded":
			if strings.TrimSpace(value.Public.Answer) == "" || value.Public.ErrorCode != "" || len(value.Private.Evidence) == 0 {
				return ErrInvalidCheckpoint
			}
		case "failed":
			if value.Public.Answer != "" || strings.TrimSpace(value.Public.ErrorCode) == "" || value.Private.Error == "" {
				return ErrInvalidCheckpoint
			}
		default:
			return ErrInvalidCheckpoint
		}
		if value.Public.TotalTokens != value.Public.InputTokens+value.Public.OutputTokens || value.Public.CachedTokens > value.Public.InputTokens {
			return ErrInvalidCheckpoint
		}
	}
	return nil
}

func SaveCheckpoint(path string, checkpoint Checkpoint) error {
	if !absoluteCheckpointPath(path) {
		return ErrInvalidCheckpoint
	}
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".answer-checkpoint-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func LoadCheckpoint(path string) (Checkpoint, error) {
	if !absoluteCheckpointPath(path) {
		return Checkpoint{}, ErrInvalidCheckpoint
	}
	file, err := os.Open(path)
	if err != nil {
		return Checkpoint{}, err
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, (64<<20)+1))
	if err != nil || len(encoded) == 0 || len(encoded) > 64<<20 {
		return Checkpoint{}, ErrInvalidCheckpoint
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var checkpoint Checkpoint
	if err := decoder.Decode(&checkpoint); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Checkpoint{}, ErrInvalidCheckpoint
	}
	if err := checkpoint.Validate(); err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}

func checkpointDigest(checkpoint Checkpoint) (string, error) {
	checkpoint.Hash = ""
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validCheckpointText(value string) bool {
	return value != "" && len(value) <= 512 && strings.TrimSpace(value) == value && !strings.ContainsRune(value, '\x00')
}

func validCheckpointDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func absoluteCheckpointPath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}
