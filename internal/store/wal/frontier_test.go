package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestDurableFrontierInitialAndCommitReopenGolden(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "redo")
	set, err := CreateSegmentSet(directory, 100)
	if err != nil {
		t.Fatal(err)
	}
	initial := mustFrontier(t, set)
	if initial != (FrontierInfo{Generation: 1, SegmentID: 1, DurableEndLSN: 164}) {
		t.Fatalf("initial frontier = %#v", initial)
	}
	for slot := 0; slot < 2; slot++ {
		info, err := os.Stat(frontierPath(directory, slot))
		if err != nil || info.Size() != frontierFileSize || info.Mode().Perm() != 0o600 {
			t.Fatalf("slot %d stat = %#v, %v", slot, info, err)
		}
	}
	encoded0, err := os.ReadFile(frontierPath(directory, 0))
	if err != nil || !bytes.Equal(encoded0, encodeFrontier(initial)) {
		t.Fatalf("initial slot 0 golden mismatch: %v", err)
	}
	encoded1, err := os.ReadFile(frontierPath(directory, 1))
	if err != nil || !allZero(encoded1) {
		t.Fatalf("initial slot 1 = %x, %v", encoded1[:16], err)
	}

	receipt, err := set.Commit(7, []Record{{Type: TypePageInit, Payload: []byte("value")}})
	if err != nil {
		t.Fatal(err)
	}
	committed := mustFrontier(t, set)
	if committed != (FrontierInfo{
		Generation: 2, SegmentID: 1, DurableEndLSN: receipt.DurableLSN,
		LastTransactionID: 7,
	}) {
		t.Fatalf("committed frontier = %#v, receipt=%#v", committed, receipt)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenSegmentSet(directory, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got := mustFrontier(t, reopened); got != committed {
		t.Fatalf("reopened frontier = %#v, want %#v", got, committed)
	}
	transactions, err := reopened.ScanCommitted()
	if err != nil || len(transactions) != 1 || transactions[0].Receipt.TransactionID != 7 {
		t.Fatalf("reopened transactions = %#v, %v", transactions, err)
	}
}

func TestDurableFrontierCheckpointAndRollAdvanceMonotonically(t *testing.T) {
	set, err := CreateSegmentSet(filepath.Join(t.TempDir(), "redo"), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close() })
	receipt, err := set.Commit(1, []Record{{Type: TypePageInit}})
	if err != nil {
		t.Fatal(err)
	}
	afterCommit := mustFrontier(t, set)
	if afterCommit.Generation != 2 || afterCommit.DurableEndLSN != receipt.DurableLSN ||
		afterCommit.LastTransactionID != 1 {
		t.Fatalf("after Commit = %#v", afterCommit)
	}
	rolled, err := set.Roll()
	if err != nil {
		t.Fatal(err)
	}
	afterRoll := mustFrontier(t, set)
	if afterRoll.Generation != 3 || afterRoll.SegmentID != 2 ||
		afterRoll.DurableEndLSN != rolled.NextLSN || afterRoll.LastTransactionID != 1 {
		t.Fatalf("after Roll = %#v, roll=%#v", afterRoll, rolled)
	}
	checkpoint, err := set.PublishCheckpoint(&recordingBarrier{})
	if err != nil {
		t.Fatal(err)
	}
	afterCheckpoint := mustFrontier(t, set)
	if afterCheckpoint.Generation != 4 || afterCheckpoint.SegmentID != 2 ||
		afterCheckpoint.DurableEndLSN != checkpoint.DurableLSN ||
		afterCheckpoint.LastTransactionID != 1 {
		t.Fatalf("after Checkpoint = %#v, checkpoint=%#v", afterCheckpoint, checkpoint)
	}
}

func TestDurableFrontierCommitFaultsReturnOutcomeUnknown(t *testing.T) {
	for _, name := range []string{
		"WAL sync", "frontier write", "frontier partial header",
		"frontier short write with complete header", "frontier sync",
	} {
		name := name
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "redo")
			set, err := CreateSegmentSet(directory, 0)
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected " + name)
			switch name {
			case "WAL sync":
				active := set.segments[0]
				active.file = &transactionFaultFile{segmentFile: active.file, syncErr: injected}
			case "frontier write":
				set.frontierFiles[1] = &transactionFaultFile{
					segmentFile: set.frontierFiles[1], failWriteCall: 1, writeErr: injected,
				}
			case "frontier partial header":
				set.frontierFiles[1] = &shortFrontierFile{
					segmentFile: set.frontierFiles[1], writeBytes: 32,
				}
				injected = io.ErrShortWrite
			case "frontier short write with complete header":
				set.frontierFiles[1] = &shortFrontierFile{
					segmentFile: set.frontierFiles[1], writeBytes: 127,
				}
				injected = io.ErrShortWrite
			case "frontier sync":
				set.frontierFiles[1] = &transactionFaultFile{
					segmentFile: set.frontierFiles[1], syncErr: injected,
				}
			}
			if _, err := set.Commit(1, []Record{{Type: TypePageInit}}); !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, injected) {
				t.Fatalf("Commit error = %v, want outcome unknown + injected", err)
			}
			if got := mustFrontier(t, set); got.Generation != 1 {
				t.Fatalf("in-memory frontier advanced: %#v", got)
			}
			if _, err := set.Commit(2, []Record{{Type: TypePageInit}}); !errors.Is(err, ErrPoisoned) {
				t.Fatalf("Commit after fault = %v, want ErrPoisoned", err)
			}
			if err := set.Close(); err != nil {
				t.Fatal(err)
			}
			opened, openErr := OpenSegmentSet(directory, 0)
			if name == "frontier sync" || name == "frontier short write with complete header" {
				if openErr != nil {
					t.Fatalf("sync outcome should be decided by valid new slot: %v", openErr)
				}
				if got := mustFrontier(t, opened); got.Generation != 2 || got.LastTransactionID != 1 {
					t.Fatalf("recovered sync outcome = %#v", got)
				}
				_ = opened.Close()
			} else {
				if openErr != nil {
					t.Fatalf("Open after speculative tail = %v", openErr)
				}
				if got := mustFrontier(t, opened); got.Generation != 1 ||
					got.LastTransactionID != 0 {
					t.Fatalf("repaired speculative outcome = %#v", got)
				}
				transactions, err := opened.ScanCommitted()
				if err != nil || len(transactions) != 0 {
					t.Fatalf("transactions after repair = %#v, %v", transactions, err)
				}
				_ = opened.Close()
			}
		})
	}
}

func TestDurableFrontierCheckpointAndRollPublishFaultsPoison(t *testing.T) {
	t.Run("checkpoint", func(t *testing.T) {
		set := checkpointTestSet(t)
		before := mustFrontier(t, set)
		target := 1 - set.frontier.slot
		injected := errors.New("injected checkpoint frontier")
		set.frontierFiles[target] = &transactionFaultFile{
			segmentFile: set.frontierFiles[target], failWriteCall: 1, writeErr: injected,
		}
		if _, err := set.PublishCheckpoint(&recordingBarrier{}); !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, injected) {
			t.Fatalf("PublishCheckpoint error = %v", err)
		}
		if got := mustFrontier(t, set); got != before {
			t.Fatalf("checkpoint fault advanced frontier: %#v", got)
		}
		assertNoCheckpoint(t, set)
	})

	t.Run("roll", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "redo")
		set, err := CreateSegmentSet(directory, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := set.Commit(1, []Record{{Type: TypePageInit}}); err != nil {
			t.Fatal(err)
		}
		before := mustFrontier(t, set)
		target := 1 - set.frontier.slot
		injected := errors.New("injected roll frontier")
		set.frontierFiles[target] = &transactionFaultFile{
			segmentFile: set.frontierFiles[target], failWriteCall: 1, writeErr: injected,
		}
		if _, err := set.Roll(); !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, injected) {
			t.Fatalf("Roll error = %v", err)
		}
		if got := mustFrontier(t, set); got != before {
			t.Fatalf("roll fault advanced frontier: %#v", got)
		}
		if err := set.Close(); err != nil {
			t.Fatal(err)
		}
		opened, err := OpenSegmentSet(directory, 0)
		if err != nil {
			t.Fatalf("Open after unfrontiered Segment = %v", err)
		}
		state, err := opened.State()
		if err != nil || len(state) != 1 || state[0].ID != 1 {
			t.Fatalf("repaired state = %#v, %v", state, err)
		}
		_ = opened.Close()
	})
}

func TestDurableFrontierOpenRejectsCorruptionAndAuthorityMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, directory string, frontier FrontierInfo)
		want   error
	}{
		{
			name: "both slots corrupt", want: ErrCorrupt,
			mutate: func(t *testing.T, directory string, _ FrontierInfo) {
				flipFileByte(t, frontierPath(directory, 0), 20)
				flipFileByte(t, frontierPath(directory, 1), 20)
			},
		},
		{
			name: "unknown version", want: ErrUnsupportedVersion,
			mutate: func(t *testing.T, directory string, _ FrontierInfo) {
				for slot := 0; slot < 2; slot++ {
					encoded := make([]byte, frontierFileSize)
					copy(encoded[:8], frontierMagic[:])
					binary.LittleEndian.PutUint16(encoded[8:10], frontierVersion+1)
					binary.LittleEndian.PutUint16(encoded[10:12], frontierHeaderSize)
					binary.LittleEndian.PutUint32(encoded[frontierCRCOffset:], checksum(encoded, frontierCRCOffset))
					writeFixture(t, frontierPath(directory, slot), encoded)
				}
			},
		},
		{
			name: "conflicting generation", want: ErrCorrupt,
			mutate: func(t *testing.T, directory string, frontier FrontierInfo) {
				conflict := frontier
				conflict.LastTransactionID++
				writeFixture(t, frontierPath(directory, 0), encodeFrontier(conflict))
			},
		},
		{
			name: "short slot", want: ErrCorrupt,
			mutate: func(t *testing.T, directory string, _ FrontierInfo) {
				if err := os.Truncate(frontierPath(directory, 1), frontierFileSize-1); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing slot", want: ErrUnsupportedVersion,
			mutate: func(t *testing.T, directory string, _ FrontierInfo) {
				if err := os.Remove(frontierPath(directory, 1)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "frontier beyond file", want: ErrCorrupt,
			mutate: func(t *testing.T, directory string, frontier FrontierInfo) {
				frontier.Generation++
				frontier.DurableEndLSN++
				writeFixture(t, frontierPath(directory, 0), encodeFrontier(frontier))
			},
		},
		{
			name: "frontier behind file is repaired",
			mutate: func(t *testing.T, directory string, frontier FrontierInfo) {
				frontier.Generation++
				frontier.DurableEndLSN = segmentHeaderSize
				frontier.LastTransactionID = 0
				writeFixture(t, frontierPath(directory, 0), encodeFrontier(frontier))
			},
		},
		{
			name: "missing frontier Segment", want: ErrCorrupt,
			mutate: func(t *testing.T, directory string, frontier FrontierInfo) {
				frontier.Generation++
				frontier.SegmentID = 2
				writeFixture(t, frontierPath(directory, 0), encodeFrontier(frontier))
			},
		},
		{
			name: "unknown last transaction", want: ErrCorrupt,
			mutate: func(t *testing.T, directory string, frontier FrontierInfo) {
				frontier.Generation++
				frontier.LastTransactionID = 999
				writeFixture(t, frontierPath(directory, 0), encodeFrontier(frontier))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory, frontier := committedFrontierFixture(t)
			test.mutate(t, directory, frontier)
			opened, err := OpenSegmentSet(directory, 0)
			if opened != nil {
				_ = opened.Close()
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("Open error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDurableFrontierCodecSeedCorpusAndOldFormat(t *testing.T) {
	value := FrontierInfo{
		Generation: 9, SegmentID: 3, DurableEndLSN: 8_192,
		LastTransactionID: 44,
	}
	encoded := encodeFrontier(value)
	decoded, err := decodeFrontier(encoded)
	if err != nil || decoded != value {
		t.Fatalf("frontier round trip = %#v, %v", decoded, err)
	}
	const seed = int64(9711)
	random := rand.New(rand.NewSource(seed))
	for trial := 0; trial < 128; trial++ {
		corrupt := bytes.Clone(encoded)
		offset := random.Intn(len(corrupt))
		corrupt[offset] ^= byte(1 + random.Intn(255))
		if _, err := decodeFrontier(corrupt); !errors.Is(err, ErrCorrupt) &&
			!errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("seed=%d trial=%d offset=%d error=%v", seed, trial, offset, err)
		}
	}

	directory := filepath.Join(t.TempDir(), "old-redo")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	segment, err := Create(segmentPath(directory, 1), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := segment.Close(); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenSegmentSet(directory, 0); !errors.Is(err, ErrUnsupportedVersion) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatalf("old-format Open error = %v, want ErrUnsupportedVersion", err)
	}
}

func TestDurableFrontierFallsBackToOlderValidSlotAndRepairsTail(t *testing.T) {
	directory, _ := committedFrontierFixture(t)
	flipFileByte(t, frontierPath(directory, 1), 20)
	files, state, err := openFrontierControls(directory)
	if err != nil {
		t.Fatal(err)
	}
	if state.Generation != 1 || state.slot != 0 {
		t.Fatalf("fallback frontier = %#v", state)
	}
	_ = closeFrontierFiles(files)
	opened, err := OpenSegmentSet(directory, 0)
	if err != nil {
		t.Fatalf("Open with bytes beyond fallback = %v", err)
	}
	defer func() { _ = opened.Close() }()
	if got := mustFrontier(t, opened); got.Generation != 1 || got.LastTransactionID != 0 {
		t.Fatalf("fallback frontier after repair = %#v", got)
	}
	transactions, err := opened.ScanCommitted()
	if err != nil || len(transactions) != 0 {
		t.Fatalf("transactions after fallback repair = %#v, %v", transactions, err)
	}
}

func TestDurableFrontierRepairsCompleteUnpublishedCommit(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "redo")
	set, err := CreateSegmentSet(directory, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.Commit(1, []Record{{Type: TypePageInit}}); err != nil {
		t.Fatal(err)
	}
	raw, err := NewTransactionWriter(set.segments[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Commit(2, []Record{{Type: TypePageDelta}}); err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenSegmentSet(directory, 0)
	if err != nil {
		t.Fatalf("Open complete unpublished commit = %v", err)
	}
	defer func() { _ = opened.Close() }()
	transactions, err := opened.ScanCommitted()
	if err != nil {
		t.Fatal(err)
	}
	assertTransactionIDs(t, transactions, 1)
}

func TestDurableFrontierSubprocessResponseLoss(t *testing.T) {
	if directory := os.Getenv("MEMORA_FRONTIER_HELPER_DIR"); directory != "" {
		set, err := CreateSegmentSet(directory, 500)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if _, err := set.Commit(77, []Record{{Type: TypePageInit}}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		os.Exit(0)
	}
	directory := filepath.Join(t.TempDir(), "redo")
	command := exec.Command(os.Args[0], "-test.run=^TestDurableFrontierSubprocessResponseLoss$")
	command.Env = append(os.Environ(), "MEMORA_FRONTIER_HELPER_DIR="+directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper failed: %v\n%s", err, output)
	}
	set, err := OpenSegmentSet(directory, 500)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close() })
	frontier := mustFrontier(t, set)
	if frontier.LastTransactionID != 77 || frontier.Generation != 2 {
		t.Fatalf("response-loss frontier = %#v", frontier)
	}
}

func TestDurableFrontierConcurrentCommitsAndClosedState(t *testing.T) {
	set, err := CreateSegmentSet(filepath.Join(t.TempDir(), "redo"), 0)
	if err != nil {
		t.Fatal(err)
	}
	const commits = 32
	var group sync.WaitGroup
	for id := uint64(1); id <= commits; id++ {
		group.Add(1)
		go func(id uint64) {
			defer group.Done()
			if _, err := set.Commit(id, []Record{{Type: TypePageInit, PageID: id}}); err != nil {
				t.Errorf("Commit(%d): %v", id, err)
			}
		}(id)
	}
	group.Wait()
	frontier := mustFrontier(t, set)
	if frontier.Generation != commits+1 || frontier.LastTransactionID == 0 {
		t.Fatalf("concurrent frontier = %#v", frontier)
	}
	state, err := set.State()
	if err != nil || len(state) != 1 || frontier.DurableEndLSN != state[0].NextLSN {
		t.Fatalf("frontier/state = %#v / %#v, %v", frontier, state, err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := set.DurableFrontier(); !errors.Is(err, ErrSegmentSetClosed) {
		t.Fatalf("DurableFrontier after Close = %v", err)
	}
}

func committedFrontierFixture(t *testing.T) (string, FrontierInfo) {
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
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	return directory, frontier
}

func mustFrontier(t *testing.T, set *SegmentSet) FrontierInfo {
	t.Helper()
	value, err := set.DurableFrontier()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func flipFileByte(t *testing.T, path string, offset int64) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	value := []byte{0}
	if _, err := file.ReadAt(value, offset); err != nil {
		t.Fatal(err)
	}
	value[0] ^= 0xff
	if _, err := file.WriteAt(value, offset); err != nil {
		t.Fatal(err)
	}
}

type shortFrontierFile struct {
	segmentFile
	writeBytes int
}

func (file *shortFrontierFile) WriteAt(value []byte, offset int64) (int, error) {
	want := file.writeBytes
	if want > len(value) {
		want = len(value)
	}
	if _, err := file.segmentFile.WriteAt(value[:want], offset); err != nil {
		return 0, err
	}
	return want, nil
}
