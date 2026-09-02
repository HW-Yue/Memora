package native

import (
	"fmt"
	"path/filepath"
	"testing"
)

// TestEnumerationReadsTheFileNotAResidentIndex is stage one of deleting every
// process-resident index over the record log.
//
// IDs and Records used to iterate f.records, which is why that map had to hold
// an entry for every record the file had ever seen — one per write, forever,
// with no capacity and no eviction (architecture principle four, criterion 3).
// They now walk the log on demand, so enumeration no longer keeps the map
// alive. This test proves that by emptying the map and asserting both surfaces
// still answer correctly: if either still read it, both would come back empty.
func TestEnumerationReadsTheFileNotAResidentIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	// A committed transaction, a standalone Put, and an aborted transaction.
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if err := transaction.Put(
			ObjectKindRow, 1, fmt.Sprintf("row-%d", index), []byte(`{"a":1}`),
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := file.Put(ObjectKindConfiguration, 2, "budgets_r1", []byte(`{"b":2}`)); err != nil {
		t.Fatal(err)
	}

	file.mu.Lock()
	file.records = map[recordKey]recordMeta{}
	file.mu.Unlock()

	ids, err := file.IDs(ObjectKindRow)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != "row-0" || ids[2] != "row-2" {
		t.Fatalf("IDs with an empty resident map = %v, want the three committed Rows", ids)
	}
	if configuration, err := file.IDs(ObjectKindConfiguration); err != nil {
		t.Fatal(err)
	} else if len(configuration) != 1 || configuration[0] != "budgets_r1" {
		t.Fatalf("IDs(Configuration) = %v, want the standalone Put", configuration)
	}

	records, err := file.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 {
		t.Fatalf("Records() returned %d records, want 4 committed records", len(records))
	}
	for _, record := range records {
		if record.PayloadLength == 0 || record.SchemaVersion == 0 {
			t.Fatalf("record %+v lost its header fields", record)
		}
	}
}

// TestEnumerationDropsAnUncommittedTail pins the half of the walk that is easy
// to get wrong: records written inside a transaction that never committed are
// physically present in the log but are not records.
func TestEnumerationDropsAnUncommittedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	if err := file.Put(ObjectKindRow, 1, "kept", []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	// Append a BEGIN and a record with no COMMIT, exactly as a crash would.
	if _, err := file.appendRecord(objectKindTransactionBegin, 1, "tx-1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := file.appendRecord(ObjectKindRow, 1, "dropped", []byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}

	ids, err := file.IDs(ObjectKindRow)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "kept" {
		t.Fatalf("IDs = %v, want only the committed Row", ids)
	}
}
