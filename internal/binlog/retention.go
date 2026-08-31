package binlog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	filePrefix = "binlog-"
	fileSuffix = ".log"
	// sequenceWidth is how the sequence number is written into a file name.
	// Fixed width so lexical order and numeric order are the same, which is
	// what lets a directory listing be sorted as a sequence.
	sequenceWidth = 20

	// DefaultSegmentBytes is how large a file grows before the log rolls.
	//
	// The rolled file is the unit of retention: nothing can be dropped until it
	// is closed, so the segment size is the granularity of "how far back". Small
	// enough that a personal-scale Database rolls regularly, large enough that
	// a month is a handful of files rather than thousands.
	DefaultSegmentBytes = int64(16) << 20

	// DefaultRetention is how long a rolled file is kept.
	//
	// 30 days, matching MySQL 8.0's binlog_expire_logs_seconds default. Keeping
	// the log is the whole point — how far back a restore reaches is how much
	// log is still there — but "keep it" is not "keep it forever with no
	// policy": older MySQL defaulted to never expiring and that filled disks.
	//
	// The window has a consequence worth stating: a base snapshot older than it
	// can no longer be rolled forward, because the log that would carry it has
	// been dropped. A snapshot retention policy has to be aligned with this
	// number, or a Database keeps a snapshot it can no longer recover from.
	DefaultRetention = 30 * 24 * time.Hour
)

// Options configures a log's rolling and retention.
type Options struct {
	// SegmentBytes is the size a file reaches before the log rolls. Zero uses
	// DefaultSegmentBytes.
	SegmentBytes int64
	// Retention is how long a rolled file is kept. Zero uses DefaultRetention;
	// a negative value keeps every file forever.
	Retention time.Duration
}

func (options Options) segmentBytes() int64 {
	if options.SegmentBytes <= 0 {
		return DefaultSegmentBytes
	}
	return options.SegmentBytes
}

func (options Options) retention() time.Duration {
	if options.Retention == 0 {
		return DefaultRetention
	}
	return options.Retention
}

func segmentName(sequence uint64) string {
	return fmt.Sprintf("%s%0*d%s", filePrefix, sequenceWidth, sequence, fileSuffix)
}

// segmentSequence reads a file name's sequence number, and reports whether the
// name is one of the log's files at all.
func segmentSequence(name string) (uint64, bool) {
	if !strings.HasPrefix(name, filePrefix) || !strings.HasSuffix(name, fileSuffix) {
		return 0, false
	}
	digits := strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix)
	if len(digits) != sequenceWidth {
		return 0, false
	}
	sequence, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || sequence == 0 {
		return 0, false
	}
	return sequence, true
}

// segments lists the log's files in sequence order.
func segments(directory string) ([]uint64, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read binlog directory: %w", err)
	}
	sequences := make([]uint64, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if sequence, ok := segmentSequence(entry.Name()); ok {
			sequences = append(sequences, sequence)
		}
	}
	sort.Slice(sequences, func(left, right int) bool { return sequences[left] < sequences[right] })
	return sequences, nil
}

// prune drops files that have aged out of the retention window.
//
// It walks oldest first and stops at the first file inside the window, so what
// it removes is always a prefix of the sequence. That is the invariant replay
// depends on: a shorter log reaches less far back, which is the point of a
// window, but a log with a hole in the middle replays a Database that never
// existed. Dropping only a prefix makes the hole impossible rather than
// unlikely.
//
// The active file is never a candidate: it is still being written.
func prune(directory string, sequences []uint64, active uint64, window time.Duration) error {
	if window < 0 {
		return nil
	}
	deadline := time.Now().Add(-window)
	for _, sequence := range sequences {
		if sequence >= active {
			return nil
		}
		path := filepath.Join(directory, segmentName(sequence))
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat binlog segment: %w", err)
		}
		if !info.ModTime().Before(deadline) {
			// Inside the window. Everything after it is newer, so the scan is
			// finished — continuing would risk removing from the middle.
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove aged binlog segment: %w", err)
		}
	}
	return nil
}
