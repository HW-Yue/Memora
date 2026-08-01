package history

import (
	"errors"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/result"
)

func TestHistoryPaginationIsScopedTamperEvidentAndSnapshotBound(t *testing.T) {
	t.Parallel()
	records := historyPageFixture(3)
	first, page, err := Paginate("db\x00table\x00row_one", "", 2, records)
	if err != nil || len(first) != 2 || page.Snapshot == "" || page.NextCursor == "" {
		t.Fatalf("first page = %#v, %#v, %v", first, page, err)
	}
	second, continued, err := Paginate("db\x00table\x00row_one", page.NextCursor, 2, records)
	if err != nil || len(second) != 1 || second[0].Revision != 1 ||
		continued.Snapshot != page.Snapshot || continued.NextCursor != "" {
		t.Fatalf("second page = %#v, %#v, %v", second, continued, err)
	}

	assertPageCode(t, func() error {
		_, _, err := Paginate("db\x00table\x00row_two", page.NextCursor, 2, records)
		return err
	}(), result.CodeValidation)
	replacement := byte('A')
	if page.NextCursor[len(page.NextCursor)-1] == replacement {
		replacement = 'B'
	}
	tampered := page.NextCursor[:len(page.NextCursor)-1] + string(replacement)
	assertPageCode(t, func() error {
		_, _, err := Paginate("db\x00table\x00row_one", tampered, 2, records)
		return err
	}(), result.CodeValidation)
	changed := append([]Record{{Revision: 4, CommitSequence: 4, UpdatedAt: time.Unix(4, 0).UTC()}}, records...)
	assertPageCode(t, func() error {
		_, _, err := Paginate("db\x00table\x00row_one", page.NextCursor, 2, changed)
		return err
	}(), result.CodeRevisionConflict)
}

func historyPageFixture(count int) []Record {
	records := make([]Record, 0, count)
	for revision := count; revision > 0; revision-- {
		records = append(records, Record{
			Revision: uint64(revision), CommitSequence: uint64(revision), SchemaVersion: 1,
			Operation: OperationUpdate, State: "live", Actor: "agent:test", Source: "test",
			SourceKind: SourceConversationAssertion, Reason: "test",
			UpdatedAt: time.Unix(int64(revision), 0).UTC(),
		})
	}
	return records
}

func assertPageCode(t *testing.T, err error, want result.Code) {
	t.Helper()
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) || stable.StableCode() != string(want) {
		t.Fatalf("error = %v, want %s", err, want)
	}
}
