package nativechange

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/change"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestCommittedEnvelopeCodecRequiresTransactionAtomicity(t *testing.T) {
	value, err := change.NewEnvelope(7, time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC), change.Metadata{
		Actor: "agent:test", Source: "msql", Reason: "remember",
	}, []change.Entry{{
		ObjectKind: change.ObjectRow, DatabaseID: "db_work", TableID: "tbl_notes",
		ObjectID: "row_1", Operation: change.OperationInsert, AfterRevision: 1, SchemaVersion: 1,
		HistoryLocator: "row_1@00000000000000000001",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if value.CommitSequence != 7 || value.TransactionID == "" || value.Checksum == "" {
		t.Fatalf("NewEnvelope() = %+v", value)
	}
}

func TestStageCommitsBodyAndEnvelopeAtomicallyAndPagesByCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	repository := New(file)

	first := testEnvelope(t, 1, "row_1")
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Put(nativestore.ObjectKindOpaque, 1, "body_1", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := Stage(transaction, first); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	rolledBack := testEnvelope(t, 2, "row_2")
	transaction, err = file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Put(nativestore.ObjectKindOpaque, 1, "body_2", []byte("two")); err != nil {
		t.Fatal(err)
	}
	if err := Stage(transaction, rolledBack); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Get(nativestore.ObjectKindOpaque, "body_2"); !errors.Is(err, nativestore.ErrNotFound) {
		t.Fatalf("rolled-back body error = %v", err)
	}

	third := testEnvelope(t, 3, "row_3")
	transaction, err = file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := Stage(transaction, third); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	page, more, err := repository.ListAfter(0, 1)
	if err != nil || !more || len(page) != 1 || page[0].CommitSequence != 1 {
		t.Fatalf("first page = %+v, %v, %v", page, more, err)
	}
	page, more, err = repository.ListAfter(1, 10)
	if err != nil || more || len(page) != 1 || page[0].CommitSequence != 3 {
		t.Fatalf("second page = %+v, %v, %v", page, more, err)
	}
	if next, err := repository.NextSequence(7); err != nil || next != 8 {
		t.Fatalf("NextSequence(7) = %d, %v", next, err)
	}
}

func TestCrashTailNeverPublishesHalfEnvelope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(1); sequence <= 2; sequence++ {
		transaction, err := file.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := transaction.Put(
			nativestore.ObjectKindOpaque, 1, recordID(sequence), []byte{byte(sequence)},
		); err != nil {
			t.Fatal(err)
		}
		if err := Stage(transaction, testEnvelope(t, sequence, recordID(sequence))); err != nil {
			t.Fatal(err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, info.Size()-10); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	page, more, err := New(reopened).ListAfter(0, 10)
	if err != nil || more || len(page) != 1 || page[0].CommitSequence != 1 {
		t.Fatalf("recovered changes = %+v, %v, %v", page, more, err)
	}
	if _, err := reopened.Get(nativestore.ObjectKindOpaque, recordID(2)); !errors.Is(err, nativestore.ErrNotFound) {
		t.Fatalf("crash-tail body error = %v", err)
	}
}

func TestRepositoryRejectsCorruptEnvelope(t *testing.T) {
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := file.Put(nativestore.ObjectKindCommittedChange, 1, recordID(1), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := New(file).Get(1); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Get(corrupt) error = %v", err)
	}
	if _, err := New(file).NextSequence(0); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("NextSequence(corrupt) error = %v", err)
	}
}

func testEnvelope(t *testing.T, sequence uint64, rowID string) change.Envelope {
	t.Helper()
	value, err := change.NewEnvelope(sequence, time.Date(2026, 8, 1, 12, int(sequence), 0, 0, time.UTC), change.Metadata{
		Actor: "agent:test", Source: "msql", Reason: "remember",
	}, []change.Entry{{
		ObjectKind: change.ObjectRow, DatabaseID: "db_work", TableID: "tbl_notes",
		ObjectID: rowID, Operation: change.OperationInsert, AfterRevision: 1, SchemaVersion: 1,
		HistoryLocator: rowID + "@00000000000000000001",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
