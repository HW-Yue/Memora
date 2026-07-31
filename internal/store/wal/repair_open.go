package wal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrRecoveryRequired = errors.New("WAL recovery is required")

type recoveryOpenOperations struct {
	truncate      func(string, int64) error
	syncFile      func(segmentFile) error
	remove        func(string) error
	syncDirectory func(string) error
}

func defaultRecoveryOpenOperations() recoveryOpenOperations {
	return recoveryOpenOperations{
		truncate: os.Truncate,
		syncFile: func(file segmentFile) error {
			return file.Sync()
		},
		remove:        os.Remove,
		syncDirectory: syncDirectory,
	}
}

func openSegmentPrefixPath(
	path string,
	expectedSegmentID, expectedStartLSN uint64,
	prefixSize int64,
) (*Segment, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open WAL segment: %w", err)
	}
	segment, err := openSegmentPrefix(
		file,
		expectedSegmentID,
		expectedStartLSN,
		prefixSize,
	)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return segment, nil
}

func repairSegmentSet(
	directory string,
	frontierSegment *Segment,
	frontierPath string,
	frontierSize, physicalSize int64,
	tailEntries []os.DirEntry,
	operations recoveryOpenOperations,
) error {
	if physicalSize > frontierSize {
		if err := operations.truncate(frontierPath, frontierSize); err != nil {
			return recoveryRequired("truncate active Segment", err)
		}
	}
	if err := operations.syncFile(frontierSegment.file); err != nil {
		return recoveryRequired("sync active Segment", err)
	}
	for index := len(tailEntries) - 1; index >= 0; index-- {
		path := filepath.Join(directory, tailEntries[index].Name())
		if err := operations.remove(path); err != nil {
			return recoveryRequired("remove speculative Segment", err)
		}
	}
	if err := operations.syncDirectory(directory); err != nil {
		return recoveryRequired("sync WAL Segment Set directory", err)
	}
	return nil
}

func recoveryRequired(operation string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrRecoveryRequired, operation, err)
}
