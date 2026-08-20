package nativerow

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/history"
)

// sweepsToReadRow measures the full-file sweeps one Row read costs against a
// Row with the given number of revisions.
func sweepsToReadRow(t *testing.T, revisions int) uint64 {
	t.Helper()
	file, repository, current := chainFixture(t, revisions)
	before := file.Enumerations()
	value, err := repository.ReadIncludingDeleted(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if value.Revision != uint64(revisions) {
		t.Fatalf("ReadIncludingDeleted() revision = %d, want %d", value.Revision, revisions)
	}
	return file.Enumerations() - before
}

// sweepsToReadHistory measures the same for one Row's full history.
func sweepsToReadHistory(t *testing.T, revisions int) uint64 {
	t.Helper()
	file, repository, current := chainFixture(t, revisions)
	before := file.Enumerations()
	records, err := repository.HistoryAll(current.DatabaseID, current.TableID, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != revisions {
		t.Fatalf("HistoryAll() returned %d records, want %d", len(records), revisions)
	}
	return file.Enumerations() - before
}

// TestReadingOneRowCostsTheSameAtAnyDepth pins the property that makes reads
// scale: resolving a Row by ID costs a bounded number of lookups regardless of
// how many revisions sit behind it. This is the hottest path in the engine —
// every write validates a revision through it.
//
// The remaining sweeps are the Catalog read, which is bounded by schema size
// rather than by data, so the test pins that the cost does not *grow*.
func TestReadingOneRowCostsTheSameAtAnyDepth(t *testing.T) {
	t.Parallel()

	shallow, deep := sweepsToReadRow(t, 2), sweepsToReadRow(t, 40)
	if deep != shallow {
		t.Fatalf("reading a 40-revision Row swept %d times vs %d for a 2-revision Row", deep, shallow)
	}
}

// TestReadingHistoryCostsTheSameAtAnyDepth pins the same property for SHOW
// HISTORY, which used to decode every history record in the Database to find
// the ones belonging to a single Row.
func TestReadingHistoryCostsTheSameAtAnyDepth(t *testing.T) {
	t.Parallel()

	shallow, deep := sweepsToReadHistory(t, 2), sweepsToReadHistory(t, 40)
	if deep != shallow {
		t.Fatalf("reading 40 revisions of history swept %d times vs %d for 2", deep, shallow)
	}
}

// TestHistoryStaysNewestFirstAndCarriesEveryRevision pins the observable result
// of the walk, so the rewrite cannot quietly change what callers see: newest
// first, every revision present exactly once, bodies matching their revision.
func TestHistoryStaysNewestFirstAndCarriesEveryRevision(t *testing.T) {
	t.Parallel()

	_, repository, current := chainFixture(t, 6)

	records, err := repository.HistoryAll(current.DatabaseID, current.TableID, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 6 {
		t.Fatalf("HistoryAll() returned %d records, want 6", len(records))
	}
	for index, record := range records {
		wantRevision := uint64(6 - index)
		if record.Revision != wantRevision {
			t.Fatalf("record %d revision = %d, want %d", index, record.Revision, wantRevision)
		}
		if record.RowID != current.ID || record.DatabaseID != current.DatabaseID {
			t.Fatalf("record %d identity = %#v", index, record)
		}
		wantOperation := history.OperationUpdate
		if wantRevision == 1 {
			wantOperation = history.OperationInsert
		}
		if record.Operation != wantOperation {
			t.Fatalf("record %d operation = %q, want %q", index, record.Operation, wantOperation)
		}
		if wantRevision > 1 && record.Values["col_integer"] != int64(wantRevision) {
			t.Fatalf("record %d body does not match its revision: %#v", index, record.Values)
		}
	}
}

// TestHistoryLimitStopsEarlyInsteadOfReadingEverything pins that a limited page
// walks only as far as it needs to. A caller asking for 3 of 40 revisions must
// not pay for the other 37.
func TestHistoryLimitStopsEarlyInsteadOfReadingEverything(t *testing.T) {
	t.Parallel()

	_, repository, current := chainFixture(t, 40)

	records, more, err := repository.History(current.DatabaseID, current.TableID, current.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !more {
		t.Fatal("History() must report that more revisions remain")
	}
	if len(records) != 3 {
		t.Fatalf("History() returned %d records, want 3", len(records))
	}
	for index, record := range records {
		if record.Revision != uint64(40-index) {
			t.Fatalf("record %d revision = %d, want %d", index, record.Revision, 40-index)
		}
	}
}
