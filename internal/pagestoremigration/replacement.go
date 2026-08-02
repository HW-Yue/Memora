package pagestoremigration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/store/fulltextindex"
)

type ReplacementReceipt struct {
	PreviousGeneration     string
	Generation             string
	Epoch                  uint64
	PlanDigest             string
	SourceFingerprint      string
	PreviousSnapshotSHA256 string
	SnapshotSHA256         string
	Parity                 bool
	Verified               bool
	Reused                 bool
}

type replacementPhase string

const (
	phaseReplacementStagingCreated  replacementPhase = "staging-created"
	phaseReplacementBuilt           replacementPhase = "generation-built"
	phaseReplacementSourceVerified  replacementPhase = "source-reverified"
	phaseReplacementRenamed         replacementPhase = "generation-renamed"
	phaseReplacementSynced          replacementPhase = "generation-synced"
	phaseReplacementLiveOpened      replacementPhase = "live-opened"
	phaseReplacementBeforeMarker    replacementPhase = "before-marker"
	phaseReplacementMarkerPublished replacementPhase = "marker-published"
)

type replacementOperations struct {
	mkdirTemp       func(string, string) (string, error)
	rename          func(string, string) error
	removeAll       func(string) error
	syncDirectory   func(string) error
	strictOpen      func(string) (*Generation, error)
	liveOpen        func(string) (*Generation, error)
	buildCheckpoint func(applyPhase) error
	checkpoint      func(replacementPhase) error
	marker          authorityMarkerOperations
}

func defaultReplacementOperations() replacementOperations {
	return replacementOperations{
		mkdirTemp: os.MkdirTemp, rename: os.Rename, removeAll: os.RemoveAll,
		syncDirectory: syncGenerationDirectory,
		strictOpen:    OpenGeneration, liveOpen: openLiveGeneration,
		marker: authorityMarkerOperations{
			createTemp: os.CreateTemp, rename: os.Rename, syncDir: syncGenerationDirectory,
		},
	}
}

func (authority *Authority) ReplaceGeneration(ctx context.Context) (ReplacementReceipt, error) {
	return authority.replaceGeneration(ctx, defaultReplacementOperations())
}

func (authority *Authority) replaceGeneration(
	ctx context.Context,
	operations replacementOperations,
) (receipt ReplacementReceipt, result error) {
	if authority == nil || ctx == nil || operations.mkdirTemp == nil ||
		operations.rename == nil || operations.removeAll == nil || operations.syncDirectory == nil ||
		operations.strictOpen == nil || operations.liveOpen == nil {
		return ReplacementReceipt{}, fmt.Errorf("%w: replacement request", ErrInvalid)
	}
	releaseWrite, err := authority.BeginWrite(ctx)
	if err != nil {
		return ReplacementReceipt{}, err
	}
	defer releaseWrite()
	if authority.marker.Epoch == math.MaxUint64 {
		return ReplacementReceipt{}, fmt.Errorf("%w: generation epoch exhausted", ErrTargetCorrupt)
	}
	previousSnapshotSHA256, _ := fulltextSnapshotSHA256(authority.generation.fulltext)
	plan, err := authority.reader.Build(ctx)
	if err != nil {
		return ReplacementReceipt{}, err
	}
	epoch := authority.marker.Epoch + 1
	generationName := replacementGenerationName(epoch, plan.Digest)
	if generationName == "" {
		return ReplacementReceipt{}, fmt.Errorf("%w: replacement generation identity", ErrInvalid)
	}
	target := filepath.Join(authority.directory, generationName)
	reused := false
	if _, err := os.Lstat(target); err == nil {
		if err := verifyReplacementTarget(ctx, operations.strictOpen, target, plan); err != nil {
			return ReplacementReceipt{}, err
		}
		reused = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return ReplacementReceipt{}, fmt.Errorf("inspect replacement target: %w", err)
	} else {
		staging, err := operations.mkdirTemp(authority.directory, ".page-replacement-v1.staging-")
		if err != nil {
			return ReplacementReceipt{}, fmt.Errorf("create replacement staging directory: %w", err)
		}
		defer func() {
			if err := operations.removeAll(staging); err != nil {
				result = errors.Join(result, fmt.Errorf("clean replacement staging directory: %w", err))
			}
		}()
		if err := replacementCheckpoint(ctx, operations, phaseReplacementStagingCreated); err != nil {
			return ReplacementReceipt{}, err
		}
		applier := &Applier{
			reader: authority.reader, directory: authority.directory,
			operations: applyOperations{checkpoint: operations.buildCheckpoint},
		}
		if _, err := applier.build(ctx, staging, plan); err != nil {
			return ReplacementReceipt{}, err
		}
		if err := replacementCheckpoint(ctx, operations, phaseReplacementBuilt); err != nil {
			return ReplacementReceipt{}, err
		}
		if err := authority.reader.VerifySource(ctx, plan); err != nil {
			return ReplacementReceipt{}, err
		}
		if err := replacementCheckpoint(ctx, operations, phaseReplacementSourceVerified); err != nil {
			return ReplacementReceipt{}, err
		}
		if err := operations.rename(staging, target); err != nil {
			return ReplacementReceipt{}, fmt.Errorf("publish replacement generation: %w", err)
		}
		if err := replacementCheckpoint(ctx, operations, phaseReplacementRenamed); err != nil {
			return ReplacementReceipt{}, err
		}
		if err := operations.syncDirectory(authority.directory); err != nil {
			return ReplacementReceipt{}, fmt.Errorf("sync replacement generation directory: %w", err)
		}
		if err := replacementCheckpoint(ctx, operations, phaseReplacementSynced); err != nil {
			return ReplacementReceipt{}, err
		}
	}
	if err := authority.reader.VerifySource(ctx, plan); err != nil {
		return ReplacementReceipt{}, err
	}
	strict, err := operations.strictOpen(target)
	if err != nil {
		return ReplacementReceipt{}, err
	}
	verifyErr := verifyGenerationPlan(ctx, strict, plan)
	snapshotSHA256, snapshotErr := fulltextSnapshotSHA256(strict.fulltext)
	closeErr := strict.Close()
	if verifyErr != nil || snapshotErr != nil || closeErr != nil {
		return ReplacementReceipt{}, errors.Join(verifyErr, snapshotErr, closeErr)
	}
	live, err := operations.liveOpen(target)
	if err != nil {
		return ReplacementReceipt{}, err
	}
	closeLive := true
	defer func() {
		if closeLive {
			result = errors.Join(result, live.Close())
		}
	}()
	catalogReader, err := nativecatalog.NewIndexedReader(nativecatalog.New(authority.file), live.catalog)
	if err != nil {
		return ReplacementReceipt{}, err
	}
	rowReader, err := nativerow.NewIndexedReader(
		nativerow.New(authority.file), catalogReader, live.current, live.versions,
	)
	if err != nil {
		return ReplacementReceipt{}, err
	}
	if err := replacementCheckpoint(ctx, operations, phaseReplacementLiveOpened); err != nil {
		return ReplacementReceipt{}, err
	}
	manifest, err := readManifest(target)
	if err != nil {
		return ReplacementReceipt{}, err
	}
	marker, err := newReplacementAuthorityMarker(manifest, epoch)
	if err != nil {
		return ReplacementReceipt{}, err
	}

	authority.mu.Lock()
	if err := authority.healthyLocked(ctx); err != nil {
		authority.mu.Unlock()
		return ReplacementReceipt{}, err
	}
	if err := replacementCheckpoint(ctx, operations, phaseReplacementBeforeMarker); err != nil {
		authority.mu.Unlock()
		return ReplacementReceipt{}, err
	}
	if err := writeAuthorityMarkerWithOperations(authority.directory, marker, operations.marker); err != nil {
		if errors.Is(err, ErrOutcomeUnknown) {
			authority.poisoned = true
		}
		authority.mu.Unlock()
		return ReplacementReceipt{}, err
	}
	if err := replacementCheckpoint(ctx, operations, phaseReplacementMarkerPublished); err != nil {
		authority.poisoned = true
		authority.mu.Unlock()
		return ReplacementReceipt{}, fmt.Errorf("%w: marker published before replacement swap: %v", ErrOutcomeUnknown, err)
	}
	previous := authority.generation
	previousName := authority.marker.Generation
	authority.marker = marker
	authority.generation = live
	authority.catalog = catalogReader
	authority.rows = rowReader
	closeLive = false
	authority.mu.Unlock()

	closeErr = previous.Close()
	return ReplacementReceipt{
		PreviousGeneration: previousName, Generation: generationName,
		Epoch: epoch, PlanDigest: plan.Digest, SourceFingerprint: plan.SourceFingerprint,
		PreviousSnapshotSHA256: previousSnapshotSHA256, SnapshotSHA256: snapshotSHA256,
		Parity:   previousSnapshotSHA256 != "" && previousSnapshotSHA256 == snapshotSHA256,
		Verified: true, Reused: reused,
	}, closeErr
}

const fulltextSnapshotVersion = "memora.fulltext.snapshot/v1"

type canonicalFulltextSnapshot struct {
	Version  string             `json:"version"`
	Objects  []fulltext.Object  `json:"objects"`
	Postings []fulltext.Posting `json:"postings"`
}

func fulltextSnapshotSHA256(index *fulltextindex.Index) (string, error) {
	if index == nil {
		return "", fmt.Errorf("%w: Fulltext snapshot", ErrInvalid)
	}
	objects, err := index.Objects()
	if err != nil {
		return "", err
	}
	liveObjects := objects[:0]
	for _, object := range objects {
		if object.State == fulltext.StateLive {
			liveObjects = append(liveObjects, object)
		}
	}
	postings, err := index.AllPostings()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(canonicalFulltextSnapshot{
		Version: fulltextSnapshotVersion, Objects: liveObjects, Postings: postings,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("sha256:%x", digest), nil
}

func verifyReplacementTarget(
	ctx context.Context,
	open func(string) (*Generation, error),
	target string,
	plan Plan,
) error {
	generation, err := open(target)
	if err != nil {
		return err
	}
	if generation.PlanDigest() != plan.Digest ||
		generation.SourceFingerprint() != plan.SourceFingerprint {
		_ = generation.Close()
		return ErrConflict
	}
	verifyErr := verifyGenerationPlan(ctx, generation, plan)
	return errors.Join(verifyErr, generation.Close())
}

func replacementCheckpoint(
	ctx context.Context, operations replacementOperations, phase replacementPhase,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if operations.checkpoint == nil {
		return nil
	}
	if err := operations.checkpoint(phase); err != nil {
		return fmt.Errorf("Page generation replacement %s: %w", phase, err)
	}
	return nil
}
