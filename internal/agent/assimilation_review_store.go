package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

const (
	assimilationReviewArtifactSuffix   = ".review"
	maxAssimilationReviewArtifactBytes = 1 << 20
)

type AssimilationReviewStore struct {
	mu   sync.Mutex
	root string
}

func OpenAssimilationReviewStore(root string) (*AssimilationReviewStore, error) {
	if root == "" {
		return nil, ErrInvalidAssimilationReview
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve root: %v", ErrInvalidAssimilationReview, err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("open Assimilation Review Store: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, fmt.Errorf("open Assimilation Review Store: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidAssimilationReview
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("secure Assimilation Review Store: %w", err)
	}
	return &AssimilationReviewStore{root: absolute}, nil
}

func (store *AssimilationReviewStore) Read(jobID string, extentSequence uint64) (AssimilationReviewArtifact, error) {
	if store == nil || !validSourceIntakeID(jobID, 128) || extentSequence == 0 {
		return AssimilationReviewArtifact{}, ErrInvalidAssimilationReview
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.read(jobID, extentSequence)
}

func (store *AssimilationReviewStore) record(
	artifact AssimilationReviewArtifact,
) (AssimilationReviewArtifact, error) {
	if store == nil || artifact.Validate() != nil {
		return AssimilationReviewArtifact{}, ErrInvalidAssimilationReview
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	existing, err := store.read(artifact.JobID, artifact.ExtentSequence)
	if err == nil {
		if existing.InputSHA256 != artifact.InputSHA256 || existing.SHA256 != artifact.SHA256 {
			return AssimilationReviewArtifact{}, ErrAssimilationReviewConflict
		}
		existing.Replayed = true
		return existing, nil
	}
	if !errors.Is(err, ErrAssimilationReviewNotFound) {
		return AssimilationReviewArtifact{}, err
	}
	artifact.Replayed = false
	encoded, err := json.Marshal(artifact)
	if err != nil || len(encoded) == 0 || len(encoded) > maxAssimilationReviewArtifactBytes {
		return AssimilationReviewArtifact{}, ErrInvalidAssimilationReview
	}
	temporary, err := os.CreateTemp(store.root, "review-*.tmp")
	if err != nil {
		return AssimilationReviewArtifact{}, fmt.Errorf("record Assimilation Review: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return AssimilationReviewArtifact{}, fmt.Errorf("secure Assimilation Review artifact: %w", err)
	}
	if err := writeAssimilationReviewBytes(temporary, encoded); err != nil {
		return AssimilationReviewArtifact{}, fmt.Errorf("write Assimilation Review artifact: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return AssimilationReviewArtifact{}, fmt.Errorf("sync Assimilation Review artifact: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return AssimilationReviewArtifact{}, fmt.Errorf("close Assimilation Review artifact: %w", err)
	}
	path := store.path(artifact.JobID, artifact.ExtentSequence)
	if err := os.Rename(temporaryPath, path); err != nil {
		return AssimilationReviewArtifact{}, fmt.Errorf("publish Assimilation Review artifact: %w", err)
	}
	keep = true
	if err := syncAssimilationReviewDirectory(store.root); err != nil {
		return AssimilationReviewArtifact{}, err
	}
	return cloneAssimilationReviewArtifact(artifact), nil
}

func (store *AssimilationReviewStore) read(
	jobID string,
	extentSequence uint64,
) (AssimilationReviewArtifact, error) {
	path := store.path(jobID, extentSequence)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return AssimilationReviewArtifact{}, ErrAssimilationReviewNotFound
	}
	if err != nil {
		return AssimilationReviewArtifact{}, fmt.Errorf("inspect Assimilation Review artifact: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() <= 0 || info.Size() > maxAssimilationReviewArtifactBytes {
		return AssimilationReviewArtifact{}, ErrAssimilationReviewCorrupt
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return AssimilationReviewArtifact{}, fmt.Errorf("read Assimilation Review artifact: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var artifact AssimilationReviewArtifact
	if err := decoder.Decode(&artifact); err != nil || requireJSONEOF(decoder) != nil ||
		artifact.JobID != jobID || artifact.ExtentSequence != extentSequence ||
		artifact.ReviewID != assimilationReviewID(jobID, extentSequence) || artifact.Validate() != nil {
		return AssimilationReviewArtifact{}, ErrAssimilationReviewCorrupt
	}
	artifact.Replayed = false
	return cloneAssimilationReviewArtifact(artifact), nil
}

func (store *AssimilationReviewStore) path(jobID string, extentSequence uint64) string {
	digest := sha256.Sum256([]byte(jobID + "\x00" + strconv.FormatUint(extentSequence, 10)))
	return filepath.Join(store.root, hex.EncodeToString(digest[:])+assimilationReviewArtifactSuffix)
}

func writeAssimilationReviewBytes(file *os.File, encoded []byte) error {
	for len(encoded) > 0 {
		written, err := file.Write(encoded)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		encoded = encoded[written:]
	}
	return nil
}

func syncAssimilationReviewDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("sync Assimilation Review directory: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync Assimilation Review directory: %w", syncErr)
	}
	return closeErr
}
