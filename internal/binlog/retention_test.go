package binlog_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/binlog"
)

// TestTheLogRollsAndPrunesPastTheRetentionWindow is the binlog's retention gate.
//
// The log is kept because keeping it is the point: how far back a restore can
// reach is exactly how much log is still there. But "keep it" is not "keep it
// forever with no policy" — MySQL 8.0 defaults binlog_expire_logs_seconds to 30
// days for the reason that unbounded growth fills disks, and older MySQL's
// never-expire default was a known operational trap.
//
// So the log rolls into sequence-numbered files and drops the ones that have
// aged out of the window. This pins both halves: the old files go, and what is
// left still replays as one contiguous sequence.
func TestTheLogRollsAndPrunesPastTheRetentionWindow(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "binlog")
	log, err := binlog.OpenWithOptions(directory, binlog.Options{
		SegmentBytes: 4 << 10, Retention: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("b"), 512)
	for index := 0; index < 40; index++ {
		if err := log.Append(binlog.Entry{
			TransactionID: fmt.Sprintf("txn_%03d", index),
			Records: []binlog.Record{{
				Kind: 1, SchemaVersion: 1, ID: fmt.Sprintf("obj_%03d", index), Payload: payload,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	files := logFiles(t, directory)
	if len(files) < 3 {
		t.Fatalf("the log did not roll: %v", files)
	}

	// Age the oldest two past the window. Everything else stays inside it.
	old := time.Now().Add(-40 * 24 * time.Hour)
	for _, name := range files[:2] {
		if err := os.Chtimes(filepath.Join(directory, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := binlog.OpenWithOptions(directory, binlog.Options{
		SegmentBytes: 4 << 10, Retention: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	remaining := logFiles(t, directory)
	if len(remaining) != len(files)-2 {
		t.Fatalf("after pruning: %v, want the %d newest of %v", remaining, len(files)-2, files)
	}
	for _, gone := range files[:2] {
		for _, kept := range remaining {
			if kept == gone {
				t.Fatalf("aged-out file %q survived", gone)
			}
		}
	}

	// What is left has to still be one contiguous run. A prune that took a file
	// out of the middle would leave a log that replays a hole, which is worse
	// than one that is merely shorter.
	seen := make([]string, 0, 40)
	if err := reopened.Replay(func(entry binlog.Entry) error {
		seen = append(seen, entry.TransactionID)
		return nil
	}); err != nil {
		t.Fatalf("replay after pruning: %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("pruning left nothing to replay")
	}
	for index := 1; index < len(seen); index++ {
		if seen[index] <= seen[index-1] {
			t.Fatalf("replay is not in order at %d: %v", index, seen[:index+1])
		}
	}
	// The survivors are a suffix of what was written: the newest transaction is
	// still there, and the run has no gap.
	if seen[len(seen)-1] != "txn_039" {
		t.Fatalf("newest transaction after pruning = %q", seen[len(seen)-1])
	}
	first := seen[0]
	for index, id := range seen {
		want := fmt.Sprintf("txn_%03d", index+transactionNumber(t, first))
		if id != want {
			t.Fatalf("replay has a gap: position %d = %q, want %q", index, id, want)
		}
	}
}

// TestPruningNeverRemovesAFileFromTheMiddle pins the invariant the ordering
// depends on: files age out oldest-first, and the moment one is inside the
// window the scan stops. A file kept while an older one is dropped is fine; a
// file dropped while an older one is kept is a hole.
func TestPruningNeverRemovesAFileFromTheMiddle(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "binlog")
	log, err := binlog.OpenWithOptions(directory, binlog.Options{
		SegmentBytes: 2 << 10, Retention: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("c"), 512)
	for index := 0; index < 30; index++ {
		if err := log.Append(binlog.Entry{
			TransactionID: fmt.Sprintf("txn_%03d", index),
			Records: []binlog.Record{{
				Kind: 1, SchemaVersion: 1, ID: fmt.Sprintf("obj_%03d", index), Payload: payload,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	files := logFiles(t, directory)
	if len(files) < 4 {
		t.Fatalf("need several files to test the ordering, got %v", files)
	}
	// Age a file in the MIDDLE, leaving an older one inside the window. The
	// prune must stop at the older one and leave the middle alone.
	old := time.Now().Add(-40 * 24 * time.Hour)
	middle := files[len(files)/2]
	if err := os.Chtimes(filepath.Join(directory, middle), old, old); err != nil {
		t.Fatal(err)
	}
	reopened, err := binlog.OpenWithOptions(directory, binlog.Options{
		SegmentBytes: 2 << 10, Retention: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, name := range logFiles(t, directory) {
		if name == middle {
			return
		}
	}
	t.Fatalf("an aged file in the middle was removed while older files stayed: %v", files)
}

func logFiles(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func transactionNumber(t *testing.T, id string) int {
	t.Helper()
	number := 0
	if _, err := fmt.Sscanf(id, "txn_%d", &number); err != nil {
		t.Fatalf("transaction ID %q: %v", id, err)
	}
	return number
}
