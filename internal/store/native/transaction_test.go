package native

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

func TestTransactionPublishesAllRecordsOnlyAfterCommitAndReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Put(ObjectKindOpaque, 1, "row_a", []byte("A")); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Put(ObjectKindOpaque, 1, "row_b", []byte("B")); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Get(ObjectKindOpaque, "row_a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(before commit) error = %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if got, err := file.Get(ObjectKindOpaque, "row_a"); err != nil || !bytes.Equal(got, []byte("A")) {
		t.Fatalf("Get(after commit) = %q, %v", got, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	for id, want := range map[string]string{"row_a": "A", "row_b": "B"} {
		got, err := reopened.Get(ObjectKindOpaque, id)
		if err != nil || string(got) != want {
			t.Fatalf("Get(%s) = %q, %v", id, got, err)
		}
	}
}

func TestTransactionRollbackAppendsNothing(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Put(ObjectKindOpaque, 1, "row_rollback", []byte("value")); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, err := reopened.Get(ObjectKindOpaque, "row_rollback"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(rolled back) error = %v", err)
	}
}

func TestOpenIgnoresCompleteTransactionWithoutCommit(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.appendRecord(objectKindTransactionBegin, 1, "tx_incomplete", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := file.appendRecord(ObjectKindOpaque, 1, "row_uncommitted", []byte("value")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open(incomplete transaction) error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, err := reopened.Get(ObjectKindOpaque, "row_uncommitted"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(uncommitted) error = %v", err)
	}
}

func TestOpenRejectsTransactionDigestMismatch(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.appendRecord(objectKindTransactionBegin, 1, "tx_bad", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := file.appendRecord(ObjectKindOpaque, 1, "row_bad", []byte("value")); err != nil {
		t.Fatal(err)
	}
	if _, err := file.appendRecord(objectKindTransactionCommit, 1, "tx_bad", make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Open(bad digest) error = %v, want ErrCorrupt", err)
	}
}
