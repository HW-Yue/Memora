package binlog_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/binlog"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// failingSink refuses the binlog append, which is what a crash between the
// prepare mark and the binlog write looks like to the record store: the records
// are on disk, the commit mark is not, and the log never got the frame.
type failingSink struct{ err error }

func (sink failingSink) Append(string, []nativestore.BinlogRecord) error { return sink.err }

// TestACommitThatCannotReachTheBinlogIsNotCommitted is E6 stage 2's gate.
//
// The write model orders a write as prepare → binlog → commit, so that a crash
// can be classified: stopped before the binlog write finished means the
// transaction never committed; past the commit mark means it did
// (docs/product/write-model.md §3).
//
// The record store already frames every transaction that way — BEGIN is the
// prepare, COMMIT is the commit, and a scan only publishes the records between
// them when a COMMIT with a matching digest follows. What stage 1 added was the
// binlog write *between* the two. This pins the consequence: a transaction that
// cannot reach the binlog does not become visible, and reopening the Database
// does not see it.
func TestACommitThatCannotReachTheBinlogIsNotCommitted(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	refused := errors.New("binlog is unavailable")
	file.AttachBinlog(failingSink{err: refused})

	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Put(nativestore.ObjectKindOpaque, 1, "obj_one", []byte("body")); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); !errors.Is(err, refused) {
		t.Fatalf("Commit() with an unavailable binlog = %v, want the sink's error", err)
	}
	// Not visible on this handle: the commit mark was never written.
	if _, err := file.Get(nativestore.ObjectKindOpaque, "obj_one"); !errors.Is(err, nativestore.ErrNotFound) {
		t.Fatalf("Get() after a refused binlog = %v, want ErrNotFound", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	// And not visible after a reopen either, which is the case that matters:
	// the records are physically in the file, ahead of the last commit mark.
	// The scan has to leave them there.
	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatalf("a Database with an uncommitted tail must still open: %v", err)
	}
	defer reopened.Close()
	if _, err := reopened.Get(nativestore.ObjectKindOpaque, "obj_one"); !errors.Is(err, nativestore.ErrNotFound) {
		t.Fatalf("reopened Get() = %v, want ErrNotFound", err)
	}

	// The space the abandoned transaction took is reusable: the same ID commits
	// cleanly once the binlog is available again. A tail that poisoned the ID
	// forever would turn one failed write into a permanent loss of that name.
	reopened.AttachBinlog(nil)
	second, err := reopened.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Put(nativestore.ObjectKindOpaque, 1, "obj_one", []byte("body")); err != nil {
		t.Fatal(err)
	}
	if err := second.Commit(); err != nil {
		t.Fatalf("committing the same ID after the failed attempt = %v", err)
	}
	if _, err := reopened.Get(nativestore.ObjectKindOpaque, "obj_one"); err != nil {
		t.Fatalf("Get() after the retry = %v", err)
	}
}

// TestABinlogFrameWithoutItsCommitMarkIsNotReplayedAsCommitted covers the other
// side of the same window: the frame reached the log and the commit mark did
// not.
//
// The log is allowed to hold it — that is the safe direction, and the reason
// the binlog is written first. What must not happen is the Database counting it
// as committed. So the record store and the log disagree by exactly one
// transaction here, and the record store is the one that decides.
func TestABinlogFrameWithoutItsCommitMarkIsNotReplayedAsCommitted(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	log, err := binlog.Open(filepath.Join(directory, "binlog"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	// The sink records the frame and then reports failure, so the log keeps the
	// frame while the commit mark is never written.
	file.AttachBinlog(recordThenFail{log: log, err: errors.New("crash after the frame")})

	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Put(nativestore.ObjectKindOpaque, 1, "obj_one", []byte("body")); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err == nil {
		t.Fatal("Commit() must report the failure that followed the binlog write")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	frames := 0
	if err := log.Replay(func(binlog.Entry) error { frames++; return nil }); err != nil {
		t.Fatal(err)
	}
	if frames != 1 {
		t.Fatalf("binlog holds %d frames, want the 1 that was written before the failure", frames)
	}
	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Get(nativestore.ObjectKindOpaque, "obj_one"); !errors.Is(err, nativestore.ErrNotFound) {
		t.Fatalf("a transaction with no commit mark must not be visible, got %v", err)
	}
}

type recordThenFail struct {
	log *binlog.Log
	err error
}

func (sink recordThenFail) Append(transactionID string, records []nativestore.BinlogRecord) error {
	converted := make([]binlog.Record, 0, len(records))
	for _, record := range records {
		converted = append(converted, binlog.Record{
			Kind: record.Kind, SchemaVersion: record.SchemaVersion,
			ID: record.ID, Payload: record.Payload,
		})
	}
	if err := sink.log.Append(binlog.Entry{TransactionID: transactionID, Records: converted}); err != nil {
		return err
	}
	return sink.err
}
