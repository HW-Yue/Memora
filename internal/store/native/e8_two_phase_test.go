package native_test

import (
	"errors"
	"path/filepath"
	"testing"

	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// TestAPreparedTransactionIsNotCommittedUntilCompleted is E8 stage 1's seam.
//
// Clustered promotion moves the commit point off this file and onto the page
// Trees. The window that makes it possible is between these two calls: the
// records and the binlog frame are durable, and nothing yet claims the
// transaction happened. The Trees commit in that window, and the mark this file
// writes afterwards records what they decided.
//
// So a prepared transaction that is never completed has to read as "did not
// happen", both on this handle and after a reopen. If it read as committed, the
// window would be a place a crash could commit something the Trees refused.
func TestAPreparedTransactionIsNotCommittedUntilCompleted(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Put(nativestore.ObjectKindOpaque, 1, "obj_one", []byte("body")); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Prepare(); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if _, err := file.Get(nativestore.ObjectKindOpaque, "obj_one"); !errors.Is(err, nativestore.ErrNotFound) {
		t.Fatalf("Get() after Prepare = %v, want ErrNotFound", err)
	}
	if _, ok := transaction.Location(nativestore.ObjectKindOpaque, "obj_one"); ok {
		t.Fatal("a prepared record must have no durable address yet")
	}
	if err := transaction.Prepare(); err == nil {
		t.Fatal("a second Prepare() must be refused")
	}
	if err := transaction.Complete(); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if payload, err := file.Get(nativestore.ObjectKindOpaque, "obj_one"); err != nil ||
		string(payload) != "body" {
		t.Fatalf("Get() after Complete = %q, %v", payload, err)
	}
	if _, ok := transaction.Location(nativestore.ObjectKindOpaque, "obj_one"); !ok {
		t.Fatal("a completed record must have a durable address")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Get(nativestore.ObjectKindOpaque, "obj_one"); err != nil {
		t.Fatalf("reopened Get() = %v", err)
	}
}

// TestAPrepareThatIsNeverCompletedLeavesNothingBehind is the other half: the
// crash case. The Trees refused, or the process died in the window, and the
// next open has to read the Database as if the write never started.
func TestAPrepareThatIsNeverCompletedLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Put(nativestore.ObjectKindOpaque, 1, "obj_one", []byte("body")); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Prepare(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatalf("a Database with a prepared tail must still open: %v", err)
	}
	defer reopened.Close()
	if _, err := reopened.Get(nativestore.ObjectKindOpaque, "obj_one"); !errors.Is(err, nativestore.ErrNotFound) {
		t.Fatalf("reopened Get() = %v, want ErrNotFound", err)
	}
	// The name is reusable: an abandoned prepare must not burn an ID.
	second, err := reopened.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Put(nativestore.ObjectKindOpaque, 1, "obj_one", []byte("second")); err != nil {
		t.Fatal(err)
	}
	if err := second.Commit(); err != nil {
		t.Fatalf("committing the same ID after an abandoned prepare = %v", err)
	}
}

// TestAnAbandonedPrepareInTheMiddleOfTheFileIsDroppedNotCorruption pins the
// format tolerance the two-phase split needs.
//
// A BEGIN with no COMMIT used to mean only one thing: the process died between
// them, so the span was at the end of the file and nothing followed it. That
// made a second BEGIN while one was open corruption, and the scan said so.
//
// Now a prepare that the Trees refuse is an ordinary outcome, and the next
// write lands after it. So the file has to read a span with no commit mark as
// what it is — a write that did not happen — wherever it sits, and keep going.
func TestAnAbandonedPrepareInTheMiddleOfTheFileIsDroppedNotCorruption(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	first, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Put(nativestore.ObjectKindOpaque, 1, "obj_first", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}
	abandoned, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := abandoned.Put(nativestore.ObjectKindOpaque, 1, "obj_abandoned", []byte("abandoned")); err != nil {
		t.Fatal(err)
	}
	if err := abandoned.Prepare(); err != nil {
		t.Fatal(err)
	}
	// The write after the refused one, landing past the unclaimed span.
	third, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := third.Put(nativestore.ObjectKindOpaque, 1, "obj_third", []byte("third")); err != nil {
		t.Fatal(err)
	}
	if err := third.Commit(); err != nil {
		t.Fatalf("a write after an abandoned prepare = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatalf("Open() over an abandoned span = %v, want it read as uncommitted", err)
	}
	defer reopened.Close()
	for _, id := range []string{"obj_first", "obj_third"} {
		if _, err := reopened.Get(nativestore.ObjectKindOpaque, id); err != nil {
			t.Fatalf("Get(%q) = %v, want the committed record", id, err)
		}
	}
	if _, err := reopened.Get(
		nativestore.ObjectKindOpaque, "obj_abandoned",
	); !errors.Is(err, nativestore.ErrNotFound) {
		t.Fatalf("Get(abandoned) = %v, want ErrNotFound", err)
	}
}
