package native

import (
	"fmt"
	"path/filepath"
	"testing"
)

// openReads reports how many record headers an open had to hop, against a log
// with the given number of committed transactions.
//
// A count rather than wall time: the count is what the test is about, and it is
// stable across machines in a way a duration is not.
func openReads(t *testing.T, transactions int) (int, int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < transactions; index++ {
		transaction, beginErr := file.Begin()
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if err := transaction.Put(
			ObjectKindRow, 1, fmt.Sprintf("row_%06d", index), make([]byte, 400),
		); err != nil {
			t.Fatal(err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	size, err := file.Size()
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	return reopened.recoveredRecords, size
}

// TestOpeningDoesNotGrowWithHowOftenTheDatabaseWasWritten is E8 stage 3's gate.
//
// Opening used to read every record the Database had ever held: verify it, and
// put an entry for it in a process-resident map. Both halves grew with how many
// times the Database had been written to rather than with how much it holds — a
// Row updated a hundred times was a hundred entries that never went away — and
// that is the violation the whole of E8 exists to remove
// (docs/storage/record-index-and-authority-v1.md §1: 200k writes cost 1.34s and
// 27.5 MiB, every open).
//
// So the measurement is the comparison, not the absolute: thirty times the
// history must not cost thirty times the open. What an open reads now is the
// file header, the commit hint beside it, and whatever was written after the
// hint was last flushed — none of which is a function of the log's length.
func TestOpeningDoesNotGrowWithHowOftenTheDatabaseWasWritten(t *testing.T) {
	t.Parallel()

	small, smallSize := openReads(t, 20)
	large, largeSize := openReads(t, 600)
	if largeSize < smallSize*20 {
		t.Fatalf("fixture sizes are too close to compare: %d vs %d", smallSize, largeSize)
	}
	if large > small {
		t.Fatalf(
			"opening a %d-byte log hopped %d record headers against %d for a %d-byte one",
			largeSize, large, small, smallSize,
		)
	}
}
