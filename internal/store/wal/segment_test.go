package wal

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

const testStartLSN = uint64(1000)

func TestSegmentHeaderGolden(t *testing.T) {
	t.Parallel()

	got := hex.EncodeToString(encodeSegmentHeader(
		0x0102030405060708,
		0x1112131415161718,
	))
	const want = "4d454d57414c3031010040000000000008070605040302011817161514131211000000000000000000000000000000000000000000000000000000008ea92eb4"
	if got != want {
		t.Fatalf("segment header = %s, want %s", got, want)
	}
}

func TestSegmentAppendScanSyncReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "wal_1.log")
	segment, err := Create(path, 1, testStartLSN)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	assertLSNs(t, segment, testStartLSN+64, testStartLSN+64)

	first := Record{Type: TypePageDelta, TransactionID: 11, SpaceID: 7, PageID: 3, Payload: []byte("first")}
	firstLSN, err := segment.Append(first)
	if err != nil {
		t.Fatalf("Append(first) error = %v", err)
	}
	if firstLSN != testStartLSN+64 {
		t.Fatalf("first LSN = %d, want %d", firstLSN, testStartLSN+64)
	}
	second := Record{Type: TypeFullPageImage, TransactionID: 11, SpaceID: 7, PageID: 4, Payload: []byte("second")}
	secondLSN, err := segment.Append(second)
	if err != nil {
		t.Fatalf("Append(second) error = %v", err)
	}
	if secondLSN != firstLSN+56+uint64(len(first.Payload)) {
		t.Fatalf("second LSN = %d, want contiguous", secondLSN)
	}
	next, _ := segment.NextLSN()
	durable, _ := segment.DurableLSN()
	if durable == next {
		t.Fatal("Append() advanced durable LSN before Sync")
	}

	assertRecords(t, scan(t, segment), []Record{
		withLSN(first, firstLSN),
		withLSN(second, secondLSN),
	})
	if err := segment.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	next, _ = segment.NextLSN()
	durable, _ = segment.DurableLSN()
	if durable != next {
		t.Fatalf("DurableLSN() = %d, want %d", durable, next)
	}
	if err := segment.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(path, 1, testStartLSN)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertRecords(t, scan(t, reopened), []Record{
		withLSN(first, firstLSN),
		withLSN(second, secondLSN),
	})
	assertLSNs(t, reopened, next, next)
}

func TestSegmentCreateDoesNotReplaceExisting(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "existing.log")
	want := []byte("user data")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if segment, err := Create(path, 1, 0); !errors.Is(err, fs.ErrExist) {
		if segment != nil {
			_ = segment.Close()
		}
		t.Fatalf("Create(existing) error = %v, want fs.ErrExist", err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, want) {
		t.Fatalf("existing file = %q, want %q", got, want)
	}
}

func TestSegmentRejectsCorruptionAndTruncation(t *testing.T) {
	t.Parallel()

	makeSegment := func(t *testing.T) (string, []byte) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "wal.log")
		segment, err := Create(path, 1, 0)
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if _, err := segment.Append(Record{Type: TypePageDelta, Payload: []byte("payload")}); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
		if err := segment.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		return path, data
	}

	t.Run("header bit flip", func(t *testing.T) {
		path, data := makeSegment(t)
		data[0] ^= 0xff
		writeFixture(t, path, data)
		assertOpenCorrupt(t, path)
	})
	t.Run("record bit flip", func(t *testing.T) {
		path, data := makeSegment(t)
		data[len(data)-1] ^= 0xff
		writeFixture(t, path, data)
		assertOpenCorrupt(t, path)
	})
	t.Run("partial header", func(t *testing.T) {
		path, data := makeSegment(t)
		writeFixture(t, path, data[:64+20])
		assertOpenCorrupt(t, path)
	})
	t.Run("partial payload", func(t *testing.T) {
		path, data := makeSegment(t)
		writeFixture(t, path, data[:len(data)-1])
		assertOpenCorrupt(t, path)
	})
	t.Run("valid CRC unknown type", func(t *testing.T) {
		path, data := makeSegment(t)
		record := data[segmentHeaderSize:]
		binary.LittleEndian.PutUint16(record[6:8], 99)
		binary.LittleEndian.PutUint32(record[52:56], checksum(record, 52))
		writeFixture(t, path, data)
		assertOpenCorrupt(t, path)
	})
	t.Run("valid CRC wrong LSN", func(t *testing.T) {
		path, data := makeSegment(t)
		record := data[segmentHeaderSize:]
		binary.LittleEndian.PutUint64(record[16:24], 999)
		binary.LittleEndian.PutUint32(record[52:56], checksum(record, 52))
		writeFixture(t, path, data)
		assertOpenCorrupt(t, path)
	})
}

func TestSegmentRejectsInvalidRecordAndClosedOperations(t *testing.T) {
	t.Parallel()

	segment, err := Create(filepath.Join(t.TempDir(), "wal.log"), 1, 0)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := segment.Append(Record{Type: Type(99)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Append(invalid) error = %v, want ErrInvalid", err)
	}
	if err := segment.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := segment.Close(); err != nil {
		t.Fatalf("Close(second) error = %v", err)
	}
	if _, err := segment.Append(Record{Type: TypePageInit}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Append(closed) error = %v, want ErrClosed", err)
	}
	if _, err := segment.Scan(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Scan(closed) error = %v, want ErrClosed", err)
	}
	if err := segment.Sync(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Sync(closed) error = %v, want ErrClosed", err)
	}
}

func TestSegmentConcurrentAppendHasStrictLSNOrder(t *testing.T) {
	t.Parallel()

	segment, err := Create(filepath.Join(t.TempDir(), "wal.log"), 1, 0)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = segment.Close() })

	const workers = 8
	const perWorker = 25
	lsns := make(chan uint64, workers*perWorker)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for index := 0; index < perWorker; index++ {
				lsn, err := segment.Append(Record{
					Type:          TypePageDelta,
					TransactionID: uint64(worker + 1),
					Payload:       []byte{byte(index)},
				})
				if err != nil {
					t.Errorf("Append() error = %v", err)
					return
				}
				lsns <- lsn
			}
		}(worker)
	}
	group.Wait()
	close(lsns)

	gotLSNs := make([]uint64, 0, workers*perWorker)
	for lsn := range lsns {
		gotLSNs = append(gotLSNs, lsn)
	}
	sort.Slice(gotLSNs, func(left, right int) bool { return gotLSNs[left] < gotLSNs[right] })
	for index := 1; index < len(gotLSNs); index++ {
		if gotLSNs[index] != gotLSNs[index-1]+57 {
			t.Fatalf("LSN[%d] = %d, previous %d", index, gotLSNs[index], gotLSNs[index-1])
		}
	}
	if got := len(scan(t, segment)); got != workers*perWorker {
		t.Fatalf("Scan() records = %d, want %d", got, workers*perWorker)
	}
}

func TestSegmentShortWriteAndSyncFailure(t *testing.T) {
	t.Parallel()

	t.Run("short write", func(t *testing.T) {
		file := &faultSegmentFile{writeN: 55}
		segment := &Segment{file: file, startLSN: 0, nextLSN: 64, durableLSN: 64}
		if _, err := segment.Append(Record{Type: TypePageInit}); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("Append() error = %v, want io.ErrShortWrite", err)
		}
	})
	t.Run("sync", func(t *testing.T) {
		want := errors.New("injected sync error")
		file := &faultSegmentFile{syncErr: want}
		segment := &Segment{file: file, nextLSN: 100, durableLSN: 64}
		if err := segment.Sync(); !errors.Is(err, want) {
			t.Fatalf("Sync() error = %v, want injected error", err)
		}
		durable, _ := segment.DurableLSN()
		if durable != 64 {
			t.Fatalf("DurableLSN() = %d, want unchanged 64", durable)
		}
	})
}

func scan(t *testing.T, segment *Segment) []Record {
	t.Helper()
	records, err := segment.Scan()
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	return records
}

func assertLSNs(t *testing.T, segment *Segment, next, durable uint64) {
	t.Helper()
	gotNext, err := segment.NextLSN()
	if err != nil {
		t.Fatalf("NextLSN() error = %v", err)
	}
	gotDurable, err := segment.DurableLSN()
	if err != nil {
		t.Fatalf("DurableLSN() error = %v", err)
	}
	if gotNext != next || gotDurable != durable {
		t.Fatalf("LSNs = next %d durable %d, want %d/%d", gotNext, gotDurable, next, durable)
	}
}

func withLSN(value Record, lsn uint64) Record {
	value.LSN = lsn
	return value
}

func assertRecords(t *testing.T, got, want []Record) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("records = %d, want %d", len(got), len(want))
	}
	for index := range got {
		if got[index].Type != want[index].Type ||
			got[index].LSN != want[index].LSN ||
			got[index].TransactionID != want[index].TransactionID ||
			got[index].SpaceID != want[index].SpaceID ||
			got[index].PageID != want[index].PageID ||
			!bytes.Equal(got[index].Payload, want[index].Payload) {
			t.Fatalf("record[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func writeFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func assertOpenCorrupt(t *testing.T, path string) {
	t.Helper()
	segment, err := Open(path, 1, 0)
	if segment != nil {
		_ = segment.Close()
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Open(corrupt) error = %v, want ErrCorrupt", err)
	}
}

type faultSegmentFile struct {
	writeN   int
	writeErr error
	syncErr  error
}

func (file *faultSegmentFile) ReadAt([]byte, int64) (int, error) { return 0, io.EOF }
func (file *faultSegmentFile) WriteAt([]byte, int64) (int, error) {
	return file.writeN, file.writeErr
}
func (file *faultSegmentFile) Sync() error                { return file.syncErr }
func (file *faultSegmentFile) Stat() (os.FileInfo, error) { return nil, errors.New("unused") }
func (file *faultSegmentFile) Close() error               { return nil }
