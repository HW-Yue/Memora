package native

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

// TestCommitReportsDurablePhysicalLocations pins the property the version chain
// is built on: after a commit the caller learns where each record's payload
// physically sits, and that address keeps resolving after a reopen. The address
// must be usable on its own — ReadAtLocation never consults the record map, so
// a record reachable only by address is still readable.
func TestCommitReportsDurablePhysicalLocations(t *testing.T) {
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
	if err := transaction.Put(ObjectKindOpaque, 1, "row_a", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Put(ObjectKindOpaque, 1, "row_b", []byte("second")); err != nil {
		t.Fatal(err)
	}
	if _, ok := transaction.Location(ObjectKindOpaque, "row_a"); ok {
		t.Fatal("a record has no physical location before commit")
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	first, ok := transaction.Location(ObjectKindOpaque, "row_a")
	if !ok {
		t.Fatal("commit must report the location of every record it wrote")
	}
	second, ok := transaction.Location(ObjectKindOpaque, "row_b")
	if !ok {
		t.Fatal("commit must report the location of every record it wrote")
	}
	if first.Offset == 0 || second.Offset <= first.Offset {
		t.Fatalf("locations must be distinct file addresses in write order: %#v %#v", first, second)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	payload, err := reopened.ReadAtLocation(first)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, []byte("first")) {
		t.Fatalf("ReadAtLocation = %q, want %q", payload, "first")
	}
}

// TestLocationOfCommittedRecordMatchesItsBytes pins that an address looked up by
// name and an address reported by commit are the same address, so a writer can
// point a new revision at an older one it did not write itself.
func TestLocationOfCommittedRecordMatchesItsBytes(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if err := file.Put(ObjectKindOpaque, 1, "row_a", []byte("only")); err != nil {
		t.Fatal(err)
	}

	located, err := file.Location(ObjectKindOpaque, "row_a")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := file.ReadAtLocation(located)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, []byte("only")) {
		t.Fatalf("ReadAtLocation = %q, want %q", payload, "only")
	}
	if _, err := file.Location(ObjectKindOpaque, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Location(missing) error = %v, want ErrNotFound", err)
	}
}

// TestReadAtLocationRefusesAnAddressThatDoesNotDescribeItsBytes pins that a
// physical address is self-verifying. A chain pointer read from an older record
// is untrusted input: a wrong length or a corrupt payload must fail loudly
// rather than hand back whatever bytes happen to live at that offset.
func TestReadAtLocationRefusesAnAddressThatDoesNotDescribeItsBytes(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if err := file.Put(ObjectKindOpaque, 1, "row_a", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	located, err := file.Location(ObjectKindOpaque, "row_a")
	if err != nil {
		t.Fatal(err)
	}

	wrongCRC := located
	wrongCRC.CRC++
	if _, err := file.ReadAtLocation(wrongCRC); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("ReadAtLocation(wrong CRC) error = %v, want ErrCorrupt", err)
	}
	past := located
	past.Offset += 1 << 30
	if _, err := file.ReadAtLocation(past); err == nil {
		t.Fatal("reading past the end of the file must fail")
	}
	var zero Location
	if _, err := file.ReadAtLocation(zero); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ReadAtLocation(zero) error = %v, want ErrInvalidArgument", err)
	}
}
