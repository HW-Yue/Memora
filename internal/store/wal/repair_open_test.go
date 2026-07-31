package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOpenSegmentSetRepairsActiveCrashTailAndRestoresWriter(t *testing.T) {
	tests := []struct {
		name   string
		append func(t *testing.T, set *SegmentSet, frontier FrontierInfo)
	}{
		{
			name: "complete uncommitted change",
			append: func(t *testing.T, set *SegmentSet, _ FrontierInfo) {
				t.Helper()
				if _, err := set.segments[0].Append(Record{
					Type: TypePageDelta, TransactionID: 2, Payload: []byte("tail"),
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "partial Record header",
			append: func(t *testing.T, set *SegmentSet, frontier FrontierInfo) {
				t.Helper()
				appendCrashBytes(t, set.directory, frontier, encodeRecord(Record{
					Type: TypePageDelta, LSN: frontier.DurableEndLSN, TransactionID: 2,
				})[:17])
			},
		},
		{
			name: "partial Record payload",
			append: func(t *testing.T, set *SegmentSet, frontier FrontierInfo) {
				t.Helper()
				encoded := encodeRecord(Record{
					Type: TypePageDelta, LSN: frontier.DurableEndLSN,
					TransactionID: 2, Payload: []byte("partial-payload"),
				})
				appendCrashBytes(t, set.directory, frontier, encoded[:len(encoded)-3])
			},
		},
		{
			name: "complete unpublished commit",
			append: func(t *testing.T, set *SegmentSet, _ FrontierInfo) {
				t.Helper()
				writer, err := NewTransactionWriter(set.segments[0])
				if err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Commit(2, []Record{{Type: TypePageDelta}}); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "redo")
			set, err := CreateSegmentSet(directory, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := set.Commit(1, []Record{{
				Type: TypePageInit, Payload: []byte("prefix"),
			}}); err != nil {
				t.Fatal(err)
			}
			frontier := mustFrontier(t, set)
			test.append(t, set, frontier)
			if err := set.Close(); err != nil {
				t.Fatal(err)
			}

			opened, err := OpenSegmentSet(directory, 0)
			if err != nil {
				t.Fatalf("OpenSegmentSet() error = %v", err)
			}
			transactions, err := opened.ScanCommitted()
			if err != nil {
				t.Fatal(err)
			}
			assertTransactionIDs(t, transactions, 1)
			info, err := os.Stat(segmentPath(directory, 1))
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() != int64(frontier.DurableEndLSN) {
				t.Fatalf("repaired size = %d, want %d", info.Size(), frontier.DurableEndLSN)
			}
			if _, err := opened.Commit(2, []Record{{Type: TypePageDelta}}); err != nil {
				t.Fatalf("Commit(after repair) error = %v", err)
			}
			if _, err := opened.Roll(); err != nil {
				t.Fatalf("Roll(after repair) error = %v", err)
			}
			if _, err := opened.PublishCheckpoint(&recordingBarrier{}); err != nil {
				t.Fatalf("PublishCheckpoint(after repair) error = %v", err)
			}
			if err := opened.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := OpenSegmentSet(directory, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			transactions, err = reopened.ScanCommitted()
			if err != nil {
				t.Fatal(err)
			}
			assertTransactionIDs(t, transactions, 1, 2)
		})
	}
}

func TestOpenSegmentSetValidatesAuthorityBeforeRepair(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, path string, receipt Receipt, frontier FrontierInfo)
	}{
		{
			name: "bit flip",
			mutate: func(t *testing.T, path string, _ Receipt, _ FrontierInfo) {
				t.Helper()
				flipFileByte(t, path, segmentHeaderSize+recordHeaderSize)
			},
		},
		{
			name: "truncate",
			mutate: func(t *testing.T, path string, _ Receipt, frontier FrontierInfo) {
				t.Helper()
				if err := os.Truncate(path, int64(frontier.DurableEndLSN-1)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "commit digest",
			mutate: func(t *testing.T, path string, receipt Receipt, _ FrontierInfo) {
				t.Helper()
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				offset := int(receipt.CommitLSN)
				length := int(binary.LittleEndian.Uint32(data[offset+12 : offset+16]))
				record := data[offset : offset+length]
				record[recordHeaderSize+24] ^= 0xff
				binary.LittleEndian.PutUint32(record[52:56], checksum(record, 52))
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "Record LSN",
			mutate: func(t *testing.T, path string, _ Receipt, _ FrontierInfo) {
				t.Helper()
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				offset := segmentHeaderSize
				length := int(binary.LittleEndian.Uint32(data[offset+12 : offset+16]))
				record := data[offset : offset+length]
				binary.LittleEndian.PutUint64(
					record[16:24],
					binary.LittleEndian.Uint64(record[16:24])+1,
				)
				binary.LittleEndian.PutUint32(record[52:56], checksum(record, 52))
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "redo")
			set, err := CreateSegmentSet(directory, 0)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := set.Commit(1, []Record{{
				Type: TypePageInit, Payload: []byte("prefix"),
			}})
			if err != nil {
				t.Fatal(err)
			}
			frontier := mustFrontier(t, set)
			appendCrashBytes(t, directory, frontier, []byte("non-authoritative-tail"))
			if err := set.Close(); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, segmentPath(directory, 1), receipt, frontier)
			before := snapshotWALDirectory(t, directory)

			opened, err := OpenSegmentSet(directory, 0)
			if opened != nil {
				_ = opened.Close()
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("OpenSegmentSet() error = %v, want ErrCorrupt", err)
			}
			after := snapshotWALDirectory(t, directory)
			if len(after) != len(before) {
				t.Fatalf("directory entries changed: before=%d after=%d", len(before), len(after))
			}
			for name, content := range before {
				if !bytes.Equal(after[name], content) {
					t.Fatalf("%s changed before authority validation", name)
				}
			}
		})
	}
}

func TestOpenSegmentSetRepairFaultsAreRetryable(t *testing.T) {
	t.Run("truncate", func(t *testing.T) {
		directory := activeTailFixture(t)
		injected := errors.New("injected truncate")
		operations := defaultRecoveryOpenOperations()
		operations.truncate = func(string, int64) error { return injected }
		assertRepairFailureAndRetry(t, directory, operations, injected)
	})

	t.Run("file Sync after truncate", func(t *testing.T) {
		directory := activeTailFixture(t)
		injected := errors.New("injected file Sync")
		operations := defaultRecoveryOpenOperations()
		operations.syncFile = func(segmentFile) error { return injected }
		assertRepairFailureAndRetry(t, directory, operations, injected)
	})

	t.Run("remove descending after partial progress", func(t *testing.T) {
		directory := unpublishedSegmentsFixture(t)
		injected := errors.New("injected remove")
		operations := defaultRecoveryOpenOperations()
		remove := operations.remove
		var removed []string
		operations.remove = func(path string) error {
			removed = append(removed, filepath.Base(path))
			if len(removed) == 2 {
				return injected
			}
			return remove(path)
		}
		opened, err := openSegmentSetWithOperations(directory, 0, operations)
		if opened != nil {
			_ = opened.Close()
		}
		if !errors.Is(err, ErrRecoveryRequired) || !errors.Is(err, injected) {
			t.Fatalf("Open error = %v, want recovery required + injected", err)
		}
		if len(removed) != 2 ||
			removed[0] != filepath.Base(segmentPath(directory, 4)) ||
			removed[1] != filepath.Base(segmentPath(directory, 3)) {
			t.Fatalf("remove order = %#v", removed)
		}
		assertRepairRetry(t, directory)
	})

	t.Run("directory Sync after removals", func(t *testing.T) {
		directory := unpublishedSegmentsFixture(t)
		injected := errors.New("injected directory Sync")
		operations := defaultRecoveryOpenOperations()
		operations.syncDirectory = func(string) error { return injected }
		assertRepairFailureAndRetry(t, directory, operations, injected)
	})
}

func TestOpenSegmentSetRepairsRetainedLayout(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "redo")
	set, _, _, _ := reclaimableSet(t, directory)
	if _, err := set.Reclaim(); err != nil {
		t.Fatal(err)
	}
	frontier := mustFrontier(t, set)
	if frontier.SegmentID != 2 {
		t.Fatalf("frontier = %#v", frontier)
	}
	appendCrashBytes(t, directory, frontier, []byte("retained-tail"))
	if err := os.WriteFile(segmentPath(directory, 3), []byte("new-tail"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}

	opened, err := OpenSegmentSet(directory, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	transactions, err := opened.ScanCommitted()
	if err != nil {
		t.Fatal(err)
	}
	assertTransactionIDs(t, transactions, 2)
	if _, err := opened.Commit(3, []Record{{Type: TypePageDelta}}); err != nil {
		t.Fatalf("Commit(after retained repair) error = %v", err)
	}
}

func TestOpenSegmentSetRejectsTailGapWithoutRepair(t *testing.T) {
	directory := unpublishedSegmentsFixture(t)
	if err := os.Rename(segmentPath(directory, 3), segmentPath(directory, 5)); err != nil {
		t.Fatal(err)
	}
	before := snapshotWALDirectory(t, directory)

	opened, err := OpenSegmentSet(directory, 0)
	if opened != nil {
		_ = opened.Close()
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Open error = %v, want ErrCorrupt", err)
	}
	after := snapshotWALDirectory(t, directory)
	for name, content := range before {
		if !bytes.Equal(after[name], content) {
			t.Fatalf("%s changed while rejecting Segment gap", name)
		}
	}
}

func TestOpenSegmentSetRepairsSubprocessCrashTail(t *testing.T) {
	if directory := os.Getenv("MEMORA_REPAIR_OPEN_HELPER_DIR"); directory != "" {
		set, err := CreateSegmentSet(directory, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if _, err := set.Commit(1, []Record{{Type: TypePageInit}}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		if _, err := set.segments[0].Append(Record{
			Type: TypePageDelta, TransactionID: 2, Payload: []byte("crash"),
		}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(4)
		}
		if err := set.segments[0].Sync(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(5)
		}
		os.Exit(0)
	}

	directory := filepath.Join(t.TempDir(), "redo")
	command := exec.Command(os.Args[0], "-test.run=^TestOpenSegmentSetRepairsSubprocessCrashTail$")
	command.Env = append(os.Environ(), "MEMORA_REPAIR_OPEN_HELPER_DIR="+directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper failed: %v\n%s", err, output)
	}
	opened, err := OpenSegmentSet(directory, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	transactions, err := opened.ScanCommitted()
	if err != nil {
		t.Fatal(err)
	}
	assertTransactionIDs(t, transactions, 1)
}

func TestOpenSegmentSetRemovesUnpublishedSegments(t *testing.T) {
	directory := unpublishedSegmentsFixture(t)

	opened, err := OpenSegmentSet(directory, 0)
	if err != nil {
		t.Fatalf("OpenSegmentSet() error = %v", err)
	}
	defer func() { _ = opened.Close() }()
	for segmentID := uint64(3); segmentID <= 4; segmentID++ {
		if _, err := os.Stat(segmentPath(directory, segmentID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Segment %d still exists: %v", segmentID, err)
		}
	}
	if _, err := opened.Commit(2, []Record{{Type: TypePageDelta}}); err != nil {
		t.Fatalf("Commit(after Segment repair) error = %v", err)
	}
}

func activeTailFixture(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "redo")
	set, err := CreateSegmentSet(directory, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.Commit(1, []Record{{Type: TypePageInit}}); err != nil {
		t.Fatal(err)
	}
	frontier := mustFrontier(t, set)
	appendCrashBytes(t, directory, frontier, []byte("crash-tail"))
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	return directory
}

func unpublishedSegmentsFixture(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "redo")
	set, err := CreateSegmentSet(directory, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.Commit(1, []Record{{Type: TypePageInit}}); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Roll(); err != nil {
		t.Fatal(err)
	}
	frontier := mustFrontier(t, set)
	if frontier.SegmentID != 2 {
		t.Fatalf("frontier = %#v", frontier)
	}
	for segmentID := uint64(3); segmentID <= 4; segmentID++ {
		if err := os.WriteFile(
			segmentPath(directory, segmentID),
			[]byte("partial unpublished Segment"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	return directory
}

func assertRepairFailureAndRetry(
	t *testing.T,
	directory string,
	operations recoveryOpenOperations,
	injected error,
) {
	t.Helper()
	opened, err := openSegmentSetWithOperations(directory, 0, operations)
	if opened != nil {
		_ = opened.Close()
	}
	if !errors.Is(err, ErrRecoveryRequired) || !errors.Is(err, injected) {
		t.Fatalf("Open error = %v, want recovery required + injected", err)
	}
	assertRepairRetry(t, directory)
}

func assertRepairRetry(t *testing.T, directory string) {
	t.Helper()
	opened, err := OpenSegmentSet(directory, 0)
	if err != nil {
		t.Fatalf("OpenSegmentSet(retry) error = %v", err)
	}
	defer func() { _ = opened.Close() }()
	transactions, err := opened.ScanCommitted()
	if err != nil {
		t.Fatal(err)
	}
	assertTransactionIDs(t, transactions, 1)
	if _, err := opened.Commit(2, []Record{{Type: TypePageDelta}}); err != nil {
		t.Fatalf("Commit(after retry) error = %v", err)
	}
}

func appendCrashBytes(
	t *testing.T,
	directory string,
	frontier FrontierInfo,
	content []byte,
) {
	t.Helper()
	path := segmentPath(directory, frontier.SegmentID)
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	header := make([]byte, segmentHeaderSize)
	if _, err := file.ReadAt(header, 0); err != nil {
		t.Fatal(err)
	}
	_, startLSN, err := decodeSegmentHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	offset := int64(frontier.DurableEndLSN - startLSN)
	if _, err := file.WriteAt(content, offset); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
}

func snapshotWALDirectory(t *testing.T, directory string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = content
	}
	return result
}
