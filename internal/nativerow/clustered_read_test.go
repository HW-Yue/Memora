package nativerow

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store/rowversionindex"
)

// clusteredLocator builds the leaf entry the clustered index stores for a Row:
// identity plus the encoded Row itself.
func clusteredLocator(t *testing.T, value row.Row, table catalog.Table) rowversionindex.Locator {
	t.Helper()
	body, err := EncodeBody(value, table)
	if err != nil {
		t.Fatal(err)
	}
	return rowversionindex.Locator{
		DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID,
		SchemaRevision: value.SchemaVersion, Revision: value.Revision,
		CommitSequence: value.CommitSequence, State: value.State,
		Body: string(body),
	}
}

// TestClusteredLeafResolvesWithoutTheRecordLog pins the point of the whole
// change: when the leaf carries the Row, reaching the leaf is reaching the data.
// The reader must decode what the tree handed it and never go back to the record
// log for the bytes.
func TestClusteredLeafResolvesWithoutTheRecordLog(t *testing.T) {
	t.Parallel()

	databases, value := rowFixture()
	table := databases[0].Tables[0]
	locator := clusteredLocator(t, value, table)

	got, err := RowFromLocator(locator, table)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != value.ID || got.Revision != value.Revision || got.State != value.State {
		t.Fatalf("RowFromLocator identity = %#v", got)
	}
	if got.Values["col_text"] != value.Values["col_text"] ||
		got.Values["col_integer"] != value.Values["col_integer"] {
		t.Fatalf("RowFromLocator values = %#v, want %#v", got.Values, value.Values)
	}
}

// TestClusteredLeafRefusesABodyThatContradictsItsKey pins that the body is
// checked against the identity the key promised. A leaf whose bytes describe a
// different Row is corruption, not a Row to return.
func TestClusteredLeafRefusesABodyThatContradictsItsKey(t *testing.T) {
	t.Parallel()

	databases, value := rowFixture()
	table := databases[0].Tables[0]
	locator := clusteredLocator(t, value, table)
	locator.Revision = value.Revision + 1

	if _, err := RowFromLocator(locator, table); err == nil {
		t.Fatal("a body that disagrees with its key must be refused")
	}
}

// TestLocatorWithoutABodyReportsItRatherThanGuessing pins the migration bridge:
// a Row written before the leaf carried bodies has an empty Body, and the caller
// has to fall back to the record log rather than treat empty as an empty Row.
func TestLocatorWithoutABodyReportsItRatherThanGuessing(t *testing.T) {
	t.Parallel()

	databases, value := rowFixture()
	table := databases[0].Tables[0]
	locator := clusteredLocator(t, value, table)
	locator.Body = ""

	if _, err := RowFromLocator(locator, table); err == nil {
		t.Fatal("a bodyless locator must not decode to a Row")
	}
}

// TestEncodedBodyStaysUnderTheRecordBudget pins that an ordinary Row encodes
// well inside the 8 KiB per-record budget, so the cap is a guard against
// pathological Rows rather than a limit ordinary writes bump into.
func TestEncodedBodyStaysUnderTheRecordBudget(t *testing.T) {
	t.Parallel()

	databases, value := rowFixture()
	table := databases[0].Tables[0]
	body, err := EncodeBody(value, table)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 1<<10 {
		t.Fatalf("an ordinary Row encodes to %d bytes, unexpectedly close to the budget", len(body))
	}
	if !strings.Contains(string(body), "row_fixture") {
		t.Fatal("the encoded body must carry the Row identity")
	}
}
