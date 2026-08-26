package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

const (
	frontierFileSize   = 4096
	frontierHeaderSize = uint16(64)
	frontierVersion    = uint16(1)
	frontierCRCOffset  = 60
)

var (
	ErrOutcomeUnknown = errors.New("WAL operation outcome is unknown")
	frontierMagic     = [8]byte{'M', 'E', 'M', 'W', 'D', 'F', '0', '1'}
)

type FrontierInfo struct {
	Generation        uint64
	SegmentID         uint64
	DurableEndLSN     uint64
	LastTransactionID uint64
}

type frontierState struct {
	FrontierInfo
	slot int
}

func (set *SegmentSet) DurableFrontier() (FrontierInfo, error) {
	set.mu.Lock()
	defer set.mu.Unlock()
	if set.closed {
		return FrontierInfo{}, ErrSegmentSetClosed
	}
	return set.frontier.FrontierInfo, nil
}

func (set *SegmentSet) DurableLSN() (uint64, error) {
	frontier, err := set.DurableFrontier()
	return frontier.DurableEndLSN, err
}

func createFrontierControls(
	directory string,
	initial FrontierInfo,
) ([2]segmentFile, frontierState, error) {
	var files [2]segmentFile
	if err := validateFrontierInfo(initial); err != nil {
		return files, frontierState{}, err
	}
	for slot := range files {
		path := frontierPath(directory, slot)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			closeAndRemoveFrontierFiles(directory, files)
			return [2]segmentFile{}, frontierState{}, fmt.Errorf("create durable frontier slot %d: %w", slot, err)
		}
		files[slot] = file
		encoded := make([]byte, frontierFileSize)
		if slot == 0 {
			encoded = encodeFrontier(initial)
		}
		if err := writeFullAt(file, encoded, 0); err != nil {
			closeAndRemoveFrontierFiles(directory, files)
			return [2]segmentFile{}, frontierState{}, fmt.Errorf("write durable frontier slot %d: %w", slot, err)
		}
		if err := file.Sync(); err != nil {
			closeAndRemoveFrontierFiles(directory, files)
			return [2]segmentFile{}, frontierState{}, fmt.Errorf("sync durable frontier slot %d: %w", slot, err)
		}
	}
	if err := syncDirectory(directory); err != nil {
		closeAndRemoveFrontierFiles(directory, files)
		return [2]segmentFile{}, frontierState{}, fmt.Errorf("sync durable frontier directory: %w", err)
	}
	return files, frontierState{FrontierInfo: initial, slot: 0}, nil
}

func openFrontierControls(directory string) ([2]segmentFile, frontierState, error) {
	var files [2]segmentFile
	var values [2]FrontierInfo
	var decodeErrors [2]error
	for slot := range files {
		path := frontierPath(directory, slot)
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			_ = closeFrontierFiles(files)
			if errors.Is(err, os.ErrNotExist) {
				return [2]segmentFile{}, frontierState{}, fmt.Errorf(
					"%w: durable frontier controls are missing", ErrUnsupportedVersion,
				)
			}
			return [2]segmentFile{}, frontierState{}, fmt.Errorf("open durable frontier slot %d: %w", slot, err)
		}
		files[slot] = file
		info, err := file.Stat()
		if err != nil {
			_ = closeFrontierFiles(files)
			return [2]segmentFile{}, frontierState{}, fmt.Errorf("stat durable frontier slot %d: %w", slot, err)
		}
		if !info.Mode().IsRegular() || info.Size() != frontierFileSize {
			_ = closeFrontierFiles(files)
			return [2]segmentFile{}, frontierState{}, fmt.Errorf("%w: durable frontier slot %d size", ErrCorrupt, slot)
		}
		encoded := make([]byte, frontierFileSize)
		if err := readFullAt(file, encoded, 0); err != nil {
			_ = closeFrontierFiles(files)
			return [2]segmentFile{}, frontierState{}, fmt.Errorf("read durable frontier slot %d: %w", slot, err)
		}
		values[slot], decodeErrors[slot] = decodeFrontier(encoded)
	}

	valid0, valid1 := decodeErrors[0] == nil, decodeErrors[1] == nil
	if !valid0 && !valid1 {
		_ = closeFrontierFiles(files)
		if errors.Is(decodeErrors[0], ErrUnsupportedVersion) ||
			errors.Is(decodeErrors[1], ErrUnsupportedVersion) {
			return [2]segmentFile{}, frontierState{}, fmt.Errorf("%w: durable frontier slots", ErrUnsupportedVersion)
		}
		return [2]segmentFile{}, frontierState{}, fmt.Errorf("%w: both durable frontier slots", ErrCorrupt)
	}
	selected := 0
	if !valid0 || (valid1 && values[1].Generation > values[0].Generation) {
		selected = 1
	}
	if valid0 && valid1 && values[0].Generation == values[1].Generation && values[0] != values[1] {
		_ = closeFrontierFiles(files)
		return [2]segmentFile{}, frontierState{}, fmt.Errorf("%w: conflicting durable frontier generation", ErrCorrupt)
	}
	return files, frontierState{FrontierInfo: values[selected], slot: selected}, nil
}

func (set *SegmentSet) publishFrontier(segmentID, durableEndLSN, lastTransactionID uint64) error {
	if set.frontier.Generation == math.MaxUint64 {
		return fmt.Errorf("%w: durable frontier generation overflow", ErrInvalid)
	}
	next := FrontierInfo{
		Generation: set.frontier.Generation + 1, SegmentID: segmentID,
		DurableEndLSN: durableEndLSN, LastTransactionID: lastTransactionID,
	}
	if err := validateFrontierInfo(next); err != nil {
		return err
	}
	target := 1 - set.frontier.slot
	if err := writeFullAt(set.frontierFiles[target], encodeFrontier(next), 0); err != nil {
		return fmt.Errorf("write durable frontier slot %d: %w", target, err)
	}
	if err := set.frontierFiles[target].Sync(); err != nil {
		return fmt.Errorf("sync durable frontier slot %d: %w", target, err)
	}
	set.frontier = frontierState{FrontierInfo: next, slot: target}
	return nil
}

func encodeFrontier(value FrontierInfo) []byte {
	encoded := make([]byte, frontierFileSize)
	copy(encoded[:8], frontierMagic[:])
	binary.LittleEndian.PutUint16(encoded[8:10], frontierVersion)
	binary.LittleEndian.PutUint16(encoded[10:12], frontierHeaderSize)
	binary.LittleEndian.PutUint64(encoded[16:24], value.Generation)
	binary.LittleEndian.PutUint64(encoded[24:32], value.SegmentID)
	binary.LittleEndian.PutUint64(encoded[32:40], value.DurableEndLSN)
	binary.LittleEndian.PutUint64(encoded[40:48], value.LastTransactionID)
	binary.LittleEndian.PutUint32(
		encoded[frontierCRCOffset:frontierCRCOffset+4],
		checksum(encoded, frontierCRCOffset),
	)
	return encoded
}

func decodeFrontier(encoded []byte) (FrontierInfo, error) {
	if len(encoded) != frontierFileSize ||
		binary.LittleEndian.Uint32(encoded[frontierCRCOffset:frontierCRCOffset+4]) != checksum(encoded, frontierCRCOffset) ||
		!bytes.Equal(encoded[:8], frontierMagic[:]) {
		return FrontierInfo{}, fmt.Errorf("%w: durable frontier bytes", ErrCorrupt)
	}
	if binary.LittleEndian.Uint16(encoded[8:10]) != frontierVersion {
		return FrontierInfo{}, fmt.Errorf("%w: durable frontier", ErrUnsupportedVersion)
	}
	if binary.LittleEndian.Uint16(encoded[10:12]) != frontierHeaderSize ||
		!allZero(encoded[12:16]) || !allZero(encoded[48:frontierCRCOffset]) ||
		!allZero(encoded[frontierCRCOffset+4:]) {
		return FrontierInfo{}, fmt.Errorf("%w: durable frontier fields", ErrCorrupt)
	}
	value := FrontierInfo{
		Generation:        binary.LittleEndian.Uint64(encoded[16:24]),
		SegmentID:         binary.LittleEndian.Uint64(encoded[24:32]),
		DurableEndLSN:     binary.LittleEndian.Uint64(encoded[32:40]),
		LastTransactionID: binary.LittleEndian.Uint64(encoded[40:48]),
	}
	if err := validateFrontierInfo(value); err != nil {
		return FrontierInfo{}, fmt.Errorf("%w: durable frontier identity", ErrCorrupt)
	}
	return value, nil
}

func validateFrontierInfo(value FrontierInfo) error {
	if value.Generation == 0 || value.SegmentID == 0 || value.DurableEndLSN < segmentHeaderSize {
		return fmt.Errorf("%w: durable frontier identity", ErrInvalid)
	}
	return nil
}

func frontierPath(directory string, slot int) string {
	return filepath.Join(directory, fmt.Sprintf("durable-frontier-%d.ctrl", slot))
}

func isFrontierFilename(name string) bool {
	return name == "durable-frontier-0.ctrl" || name == "durable-frontier-1.ctrl"
}

func outcomeUnknown(operation string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrOutcomeUnknown, operation, err)
}

func closeFrontierFiles(files [2]segmentFile) error {
	var result error
	for _, file := range files {
		if file != nil {
			result = errors.Join(result, file.Close())
		}
	}
	return result
}

func closeAndRemoveFrontierFiles(directory string, files [2]segmentFile) {
	_ = closeFrontierFiles(files)
	for slot, file := range files {
		if file != nil {
			_ = os.Remove(frontierPath(directory, slot))
		}
	}
}
