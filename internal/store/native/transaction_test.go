package native

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestRecordsReturnsOnlyCommittedRefsInStableOrderAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Put(ObjectKindRoute, 3, "route_b", []byte("route")); err != nil {
		t.Fatal(err)
	}
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Put(ObjectKindDatabase, 7, "db_a", []byte("database")); err != nil {
		t.Fatal(err)
	}
	refs, err := file.Records()
	if err != nil || len(refs) != 1 || refs[0].Kind != ObjectKindRoute || refs[0].SchemaVersion != 3 {
		t.Fatalf("pre-commit Records() = %#v, %v", refs, err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	want := []RecordRef{
		{Kind: ObjectKindDatabase, SchemaVersion: 7, ID: "db_a", PayloadLength: 8},
		{Kind: ObjectKindRoute, SchemaVersion: 3, ID: "route_b", PayloadLength: 5},
	}
	refs, err = file.Records()
	if err != nil || !reflect.DeepEqual(refs, want) {
		t.Fatalf("committed Records() = %#v, %v; want %#v", refs, err, want)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	refs, err = reopened.Records()
	if err != nil || !reflect.DeepEqual(refs, want) {
		t.Fatalf("reopened Records() = %#v, %v; want %#v", refs, err, want)
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

func TestRecoveryTruncatesEveryPartialTransactionPrefix(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "source.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Put(ObjectKindOpaque, 1, "baseline", []byte("safe")); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	baselineEnd := stat.Size()
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Put(ObjectKindOpaque, 1, "tx_a", []byte("A")); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Put(ObjectKindOpaque, 1, "tx_b", []byte("B")); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for length := baselineEnd; length < int64(len(data)); length++ {
		length := length
		t.Run(fmt.Sprintf("byte_%d", length), func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "recovered.memora")
			if err := os.WriteFile(target, data[:length], 0o600); err != nil {
				t.Fatal(err)
			}
			opened, err := Open(target)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			if got, err := opened.Get(ObjectKindOpaque, "baseline"); err != nil || string(got) != "safe" {
				t.Fatalf("baseline = %q, %v", got, err)
			}
			if _, err := opened.Get(ObjectKindOpaque, "tx_a"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("tx_a error = %v", err)
			}
			if err := opened.Close(); err != nil {
				t.Fatal(err)
			}
			stat, err := os.Stat(target)
			if err != nil || stat.Size() != baselineEnd {
				t.Fatalf("recovered size = %v, %v; want %d", stat, err, baselineEnd)
			}
			reopened, err := Open(target)
			if err != nil {
				t.Fatalf("second Open() error = %v", err)
			}
			_ = reopened.Close()
		})
	}
}

func TestOpenRejectsCorruptionInsideCommittedTransaction(t *testing.T) {
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
	payload := []byte("committed-unique-payload")
	if err := transaction.Put(ObjectKindOpaque, 1, "committed", payload); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	offset := bytes.Index(data, payload)
	if offset < 0 {
		t.Fatal("committed payload not found")
	}
	data[offset] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Open(corrupt committed data) error = %v", err)
	}
}
