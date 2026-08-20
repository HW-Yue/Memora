package nativerow

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// chainFixture writes one Row and then revisions it `revisions-1` times, so the
// Row ends at revision `revisions` with a complete version chain behind it.
func chainFixture(t *testing.T, revisions int) (*nativestore.File, *Repository, row.Row) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	catalogValue, value := rowFixture()
	if err := nativecatalog.New(file).Write(catalogValue); err != nil {
		t.Fatal(err)
	}
	repository := New(file)
	if err := repository.Write(value); err != nil {
		t.Fatal(err)
	}
	if err := repository.AppendHistory(value, history.OperationInsert, row.WriteMetadata{}, value.CreatedAt); err != nil {
		t.Fatal(err)
	}
	for revision := 2; revision <= revisions; revision++ {
		value.Revision = uint64(revision)
		value.UpdatedAt = value.CreatedAt.Add(time.Duration(revision) * time.Minute)
		value.Values["col_integer"] = int64(revision)
		if err := repository.WriteRevision(value); err != nil {
			t.Fatalf("WriteRevision(%d) error = %v", revision, err)
		}
		if err := repository.AppendHistory(value, history.OperationUpdate, row.WriteMetadata{}, value.UpdatedAt); err != nil {
			t.Fatal(err)
		}
	}
	return file, repository, value
}

// TestEveryRevisionPointsAtThePreviousOne pins the version chain: a revision
// carries the physical address of the revision before it, so walking a Row's
// history is a sequence of direct reads rather than a lookup per step. The last
// hop must land on revision 1 and stop there.
func TestEveryRevisionPointsAtThePreviousOne(t *testing.T) {
	t.Parallel()

	file, repository, current := chainFixture(t, 5)

	location, err := repository.RevisionLocation(current.ID, current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	seen := make([]uint64, 0, 5)
	for location.Valid() {
		payload, err := file.ReadAtLocation(location)
		if err != nil {
			t.Fatal(err)
		}
		value, err := decode(payload)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, value.Revision)
		if location, err = previousLocation(payload); err != nil {
			t.Fatal(err)
		}
	}
	want := []uint64{5, 4, 3, 2, 1}
	if len(seen) != len(want) {
		t.Fatalf("chain walked %v revisions, want %v", seen, want)
	}
	for index, revision := range want {
		if seen[index] != revision {
			t.Fatalf("chain walked %v, want %v", seen, want)
		}
	}
}

// TestRevisionOnePointsAtNothing pins the chain terminator. Revision 1 has no
// predecessor, so its pointer must be the zero Location and not, say, an
// address that happens to decode.
func TestRevisionOnePointsAtNothing(t *testing.T) {
	t.Parallel()

	file, repository, _ := chainFixture(t, 1)
	location, err := repository.RevisionLocation("row_fixture", 1)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := file.ReadAtLocation(location)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := previousLocation(payload)
	if err != nil {
		t.Fatal(err)
	}
	if previous.Valid() {
		t.Fatalf("revision 1 must terminate the chain, got %#v", previous)
	}
}

// TestVersionChainSurvivesReopen pins that the chain is a durable file address,
// not a process-local one: the pointers keep resolving in a fresh process that
// never saw the writes.
func TestVersionChainSurvivesReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	catalogValue, value := rowFixture()
	if err := nativecatalog.New(file).Write(catalogValue); err != nil {
		t.Fatal(err)
	}
	repository := New(file)
	if err := repository.Write(value); err != nil {
		t.Fatal(err)
	}
	for revision := 2; revision <= 4; revision++ {
		value.Revision = uint64(revision)
		value.Values["col_integer"] = int64(revision)
		if err := repository.WriteRevision(value); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	location, err := New(reopened).RevisionLocation("row_fixture", 4)
	if err != nil {
		t.Fatal(err)
	}
	depth := 0
	for location.Valid() {
		payload, err := reopened.ReadAtLocation(location)
		if err != nil {
			t.Fatal(err)
		}
		depth++
		if location, err = previousLocation(payload); err != nil {
			t.Fatal(err)
		}
	}
	if depth != 4 {
		t.Fatalf("chain depth after reopen = %d, want 4", depth)
	}
}

// TestChainedRowStillDecodesToTheSameRow pins that the chain pointer is
// physical metadata and changes nothing a caller sees: a revision written with
// a predecessor must decode to exactly the Row that was handed in.
func TestChainedRowStillDecodesToTheSameRow(t *testing.T) {
	t.Parallel()

	_, repository, current := chainFixture(t, 3)

	got, err := repository.ReadRevision(current.ID, current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 3 || got.Values["col_integer"] != int64(3) {
		t.Fatalf("ReadRevision() = %#v", got)
	}
	first, err := repository.ReadRevision(current.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.Values["col_integer"] != int64(-42) {
		t.Fatalf("ReadRevision(1) = %#v", first)
	}
}
