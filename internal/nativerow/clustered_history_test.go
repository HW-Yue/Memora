package nativerow

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store/rowversionindex"
)

// historyLeaf builds the clustered leaf a revision is stored as: the encoded Row
// plus the metadata it was written with.
func historyLeaf(t *testing.T, value row.Row, table catalog.Table, operation history.Operation, reason string) rowversionindex.Locator {
	t.Helper()
	locator := clusteredLocator(t, value, table)
	metadata, err := historyPayload(
		value, operation, row.WriteMetadata{Reason: reason}, value.UpdatedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	locator.History = string(metadata)
	return locator
}

// TestOneLeafAnswersBothTheRowAndItsProvenance pins that a clustered leaf is
// enough to build a history record: the Row supplies the content, the metadata
// supplies who wrote it and why. Nothing else is consulted.
func TestOneLeafAnswersBothTheRowAndItsProvenance(t *testing.T) {
	t.Parallel()

	databases, value := rowFixture()
	table := databases[0].Tables[0]
	locator := historyLeaf(t, value, table, history.OperationInsert, "first write")

	body, err := RowFromLocator(locator, table)
	if err != nil {
		t.Fatal(err)
	}
	record, err := DecodeHistoryMetadata([]byte(locator.History), body)
	if err != nil {
		t.Fatal(err)
	}
	if record.RowID != value.ID || record.Revision != value.Revision {
		t.Fatalf("record identity = %#v", record)
	}
	if record.Operation != history.OperationInsert || record.Reason != "first write" {
		t.Fatalf("record provenance = operation %q reason %q", record.Operation, record.Reason)
	}
	if record.Values["col_text"] != value.Values["col_text"] {
		t.Fatalf("record values = %#v", record.Values)
	}
	if record.State != string(value.State) || record.SchemaVersion != value.SchemaVersion {
		t.Fatalf("record shape = %#v", record)
	}
}

// TestHistoryMetadataMustDescribeTheRowItIsPairedWith pins that the two halves
// of a leaf are checked against each other. Metadata naming another revision is
// corruption, not a record to return.
func TestHistoryMetadataMustDescribeTheRowItIsPairedWith(t *testing.T) {
	t.Parallel()

	databases, value := rowFixture()
	table := databases[0].Tables[0]
	locator := historyLeaf(t, value, table, history.OperationInsert, "first write")

	other := value
	other.Revision = value.Revision + 1
	if _, err := DecodeHistoryMetadata([]byte(locator.History), other); err == nil {
		t.Fatal("metadata describing another revision must be refused")
	}
}
