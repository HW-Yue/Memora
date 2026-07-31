package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	segmentManifestName    = "segments.manifest"
	segmentManifestSize    = 96
	segmentManifestVersion = uint16(1)
	segmentManifestCRC     = 92
	manifestTempPrefix     = ".segments.manifest-"
)

var (
	ErrNoReclaimableSegments = errors.New("WAL has no reclaimable Segment")
	segmentManifestMagic     = [8]byte{'M', 'E', 'M', 'W', 'S', 'E', 'T', '1'}
)

type ReclaimReport struct {
	FirstRetainedSegmentID uint64
	DeletedSegments        uint64
	ResidualSegments       uint64
}

type retainedManifest struct {
	FirstSegmentID           uint64
	FirstStartLSN            uint64
	ReclaimedTransactionHigh uint64
	Checkpoint               Checkpoint
}

func (set *SegmentSet) Reclaim() (ReclaimReport, error) {
	set.mu.Lock()
	defer set.mu.Unlock()
	if set.closed {
		return ReclaimReport{}, ErrSegmentSetClosed
	}
	if !set.hasCheckpoint {
		return ReclaimReport{}, ErrNoReclaimableSegments
	}
	checkpointIndex := -1
	for index, segment := range set.segments {
		nextLSN, err := segment.NextLSN()
		if err != nil {
			return ReclaimReport{}, err
		}
		if set.lastCheckpoint.RecordLSN >= segment.startLSN+segmentHeaderSize &&
			set.lastCheckpoint.RecordLSN < nextLSN {
			checkpointIndex = index
			break
		}
	}
	if checkpointIndex < 0 {
		return ReclaimReport{}, fmt.Errorf("%w: checkpoint Segment", ErrCorrupt)
	}
	if checkpointIndex == 0 {
		if !set.hasRetainedManifest {
			return ReclaimReport{}, ErrNoReclaimableSegments
		}
		return set.cleanupResidualLocked()
	}

	highWater := set.reclaimedTransactionHighWater
	for _, segment := range set.segments[:checkpointIndex] {
		transactions, err := ScanCommitted(segment)
		if err != nil {
			return ReclaimReport{}, err
		}
		for _, transaction := range transactions {
			if transaction.Receipt.TransactionID > highWater {
				highWater = transaction.Receipt.TransactionID
			}
		}
	}
	firstRetained := set.segments[checkpointIndex]
	manifest := retainedManifest{
		FirstSegmentID:           firstRetained.segmentID,
		FirstStartLSN:            firstRetained.startLSN,
		ReclaimedTransactionHigh: highWater,
		Checkpoint:               set.lastCheckpoint,
	}
	encoded := encodeRetainedManifest(manifest)
	temporary, err := set.writeManifestTemp(set.directory, encoded)
	if err != nil {
		return ReclaimReport{}, fmt.Errorf("write retained manifest: %w", err)
	}
	if temporary != "" {
		defer func() { _ = os.Remove(temporary) }()
	}
	manifestPath := filepath.Join(set.directory, segmentManifestName)
	if err := set.renameManifest(temporary, manifestPath); err != nil {
		return ReclaimReport{}, fmt.Errorf("publish retained manifest: %w", err)
	}
	if err := set.syncSetDirectory(set.directory); err != nil {
		return ReclaimReport{}, fmt.Errorf("sync retained manifest directory: %w", err)
	}

	oldSegments := append([]*Segment(nil), set.segments[:checkpointIndex]...)
	set.segments = set.segments[checkpointIndex:]
	set.retainedManifest = manifest
	set.hasRetainedManifest = true
	set.reclaimedTransactionHighWater = highWater
	var result error
	var deleted uint64
	for _, segment := range oldSegments {
		result = errors.Join(result, segment.Close())
		if err := set.removeSegmentFile(segmentPath(set.directory, segment.segmentID)); err != nil {
			result = errors.Join(result, err)
		} else {
			deleted++
		}
	}
	if err := set.syncSetDirectory(set.directory); err != nil {
		result = errors.Join(result, err)
	}
	residual, residualErr := countResidualSegments(set.directory, manifest.FirstSegmentID)
	result = errors.Join(result, residualErr)
	return ReclaimReport{
		FirstRetainedSegmentID: manifest.FirstSegmentID,
		DeletedSegments:        deleted,
		ResidualSegments:       residual,
	}, result
}

func (set *SegmentSet) cleanupResidualLocked() (ReclaimReport, error) {
	entries, err := os.ReadDir(set.directory)
	if err != nil {
		return ReclaimReport{}, err
	}
	var deleted uint64
	var result error
	for _, entry := range entries {
		segmentID, parseErr := parseSegmentFilename(entry.Name())
		if parseErr != nil || segmentID >= set.retainedManifest.FirstSegmentID {
			continue
		}
		if err := set.removeSegmentFile(filepath.Join(set.directory, entry.Name())); err != nil {
			result = errors.Join(result, err)
		} else {
			deleted++
		}
	}
	if err := set.syncSetDirectory(set.directory); err != nil {
		result = errors.Join(result, err)
	}
	residual, residualErr := countResidualSegments(
		set.directory,
		set.retainedManifest.FirstSegmentID,
	)
	result = errors.Join(result, residualErr)
	return ReclaimReport{
		FirstRetainedSegmentID: set.retainedManifest.FirstSegmentID,
		DeletedSegments:        deleted,
		ResidualSegments:       residual,
	}, result
}

func configureReclaimOperations(set *SegmentSet) {
	set.writeManifestTemp = writeManifestTemporary
	set.renameManifest = os.Rename
	set.removeSegmentFile = os.Remove
	set.syncSetDirectory = syncDirectory
}

func writeManifestTemporary(directory string, encoded []byte) (string, error) {
	file, err := os.CreateTemp(directory, manifestTempPrefix)
	if err != nil {
		return "", err
	}
	path := file.Name()
	removeOnError := true
	defer func() {
		_ = file.Close()
		if removeOnError {
			_ = os.Remove(path)
		}
	}()
	if err := persistManifestFile(file, encoded); err != nil {
		return "", err
	}
	removeOnError = false
	return path, nil
}

func encodeRetainedManifest(value retainedManifest) []byte {
	encoded := make([]byte, segmentManifestSize)
	copy(encoded[0:8], segmentManifestMagic[:])
	binary.LittleEndian.PutUint16(encoded[8:10], segmentManifestVersion)
	binary.LittleEndian.PutUint16(encoded[10:12], segmentManifestSize)
	binary.LittleEndian.PutUint64(encoded[16:24], value.FirstSegmentID)
	binary.LittleEndian.PutUint64(encoded[24:32], value.FirstStartLSN)
	binary.LittleEndian.PutUint64(encoded[32:40], value.ReclaimedTransactionHigh)
	binary.LittleEndian.PutUint64(encoded[40:48], value.Checkpoint.ID)
	binary.LittleEndian.PutUint64(encoded[48:56], value.Checkpoint.RecoveryLSN)
	binary.LittleEndian.PutUint64(encoded[56:64], value.Checkpoint.CoveredSegmentID)
	binary.LittleEndian.PutUint64(encoded[64:72], value.Checkpoint.RecordLSN)
	binary.LittleEndian.PutUint64(encoded[72:80], value.Checkpoint.DurableLSN)
	binary.LittleEndian.PutUint32(encoded[segmentManifestCRC:], checksum(encoded, segmentManifestCRC))
	return encoded
}

func decodeRetainedManifest(encoded []byte) (retainedManifest, error) {
	var value retainedManifest
	if len(encoded) != segmentManifestSize ||
		!bytes.Equal(encoded[0:8], segmentManifestMagic[:]) ||
		binary.LittleEndian.Uint16(encoded[8:10]) != segmentManifestVersion ||
		binary.LittleEndian.Uint16(encoded[10:12]) != segmentManifestSize ||
		!allZero(encoded[12:16]) || !allZero(encoded[80:92]) ||
		binary.LittleEndian.Uint32(encoded[segmentManifestCRC:]) != checksum(encoded, segmentManifestCRC) {
		return value, fmt.Errorf("%w: retained manifest", ErrCorrupt)
	}
	value = retainedManifest{
		FirstSegmentID:           binary.LittleEndian.Uint64(encoded[16:24]),
		FirstStartLSN:            binary.LittleEndian.Uint64(encoded[24:32]),
		ReclaimedTransactionHigh: binary.LittleEndian.Uint64(encoded[32:40]),
		Checkpoint: Checkpoint{
			ID:               binary.LittleEndian.Uint64(encoded[40:48]),
			RecoveryLSN:      binary.LittleEndian.Uint64(encoded[48:56]),
			CoveredSegmentID: binary.LittleEndian.Uint64(encoded[56:64]),
			RecordLSN:        binary.LittleEndian.Uint64(encoded[64:72]),
			DurableLSN:       binary.LittleEndian.Uint64(encoded[72:80]),
		},
	}
	if value.FirstSegmentID == 0 || value.Checkpoint.ID == 0 ||
		(value.FirstSegmentID > 1 && value.ReclaimedTransactionHigh == 0) ||
		value.Checkpoint.RecordLSN < value.FirstStartLSN+segmentHeaderSize ||
		value.Checkpoint.DurableLSN <= value.Checkpoint.RecordLSN {
		return retainedManifest{}, fmt.Errorf("%w: retained manifest fields", ErrCorrupt)
	}
	return value, nil
}

func inspectSetDirectory(
	directory string,
	entries []os.DirEntry,
) ([]os.DirEntry, retainedManifest, bool, error) {
	var manifest retainedManifest
	hasManifest := false
	segments := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		switch {
		case isFrontierFilename(entry.Name()) && !entry.IsDir():
			continue
		case entry.Name() == segmentManifestName && !entry.IsDir():
			if hasManifest {
				return nil, manifest, false, fmt.Errorf("%w: duplicate manifest", ErrCorrupt)
			}
			encoded, err := os.ReadFile(filepath.Join(directory, entry.Name()))
			if err != nil {
				return nil, manifest, false, err
			}
			manifest, err = decodeRetainedManifest(encoded)
			if err != nil {
				return nil, manifest, false, err
			}
			hasManifest = true
		case strings.HasPrefix(entry.Name(), manifestTempPrefix) && !entry.IsDir():
			continue
		default:
			if _, err := parseSegmentFilename(entry.Name()); err != nil || entry.IsDir() {
				return nil, manifest, false, fmt.Errorf(
					"%w: invalid Segment Set entry %q",
					ErrCorrupt,
					entry.Name(),
				)
			}
			segments = append(segments, entry)
		}
	}
	if hasManifest {
		retained := segments[:0]
		for _, entry := range segments {
			segmentID, _ := parseSegmentFilename(entry.Name())
			if segmentID >= manifest.FirstSegmentID {
				retained = append(retained, entry)
			}
		}
		segments = retained
	}
	if len(segments) == 0 {
		return nil, manifest, hasManifest, fmt.Errorf("%w: empty retained Segment Set", ErrCorrupt)
	}
	return segments, manifest, hasManifest, nil
}

func countResidualSegments(directory string, firstRetained uint64) (uint64, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, err
	}
	var count uint64
	for _, entry := range entries {
		segmentID, err := parseSegmentFilename(entry.Name())
		if err == nil && segmentID < firstRetained {
			count++
		}
	}
	return count, nil
}

func persistManifestFile(file segmentFile, encoded []byte) error {
	if err := writeFullAt(file, encoded, 0); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}
