package pagestoremigration

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/row"
)

// TestASchemaChangeNeverSweepsTheRecordFile is the write-path gate.
//
// The read path stopped touching the record log's resident map when the Catalog
// bodies moved into the objects Tree, but the write path still swept: every
// schema change rebuilt the whole Catalog with Repository.Read(), and then
// stageVersion swept again once per object it wrote — so publishing a Catalog of
// N objects cost N+1 passes over every Catalog record the Database had ever
// written.
//
// Neither is needed. The current Catalog comes out of the Trees, and what each
// object was last published as is something the caller already knows.
//
// CreateTable is the whole write path — read, mutate, publish — so it is what
// this measures; ApplySchemaChangePlan reaches the same two functions.
func TestASchemaChangeNeverSweepsTheRecordFile(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	dictionary := nativecatalog.NewService(
		nativecatalog.New(file), nativecatalog.ServiceOptions{Authority: authority},
	)

	// CreateTable is the whole Catalog write path: read the current Catalog,
	// mutate it, publish it. Nothing in it may sweep.
	before := file.Enumerations()
	created, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "scanned", Purpose: "Prove the write path does not sweep",
		RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{{
			Name: "title", Type: "TEXT", Purpose: "Title",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if swept := file.Enumerations() - before; swept != 0 {
		t.Fatalf("creating one Table swept the record file %d times", swept)
	}

	// A second write, so the path that has to know what each object was last
	// published as is exercised too — the first one had no previous revision to
	// compare against.
	before = file.Enumerations()
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "scanned_again", Purpose: "Second write",
		RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{{
			Name: "title", Type: "TEXT", Purpose: "Title",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if swept := file.Enumerations() - before; swept != 0 {
		t.Fatalf("a second Catalog write swept the record file %d times", swept)
	}

	// The change is really there, read back through the Authority's own reader,
	// which has no record-log handle to fall back to.
	described, err := authority.DescribeTable(ctx, "work", created.Name)
	if err != nil {
		t.Fatalf("DescribeTable after the write: %v", err)
	}
	if described.ID != created.ID || len(described.Columns) != 1 {
		t.Fatalf("Table after the write = %#v", described)
	}
	// And the Table that was already there is untouched.
	if _, err := authority.DescribeTable(ctx, "work", table.Name); err != nil {
		t.Fatalf("the pre-existing Table after the write: %v", err)
	}
}

// TestARowWriteNeverSweepsTheRecordFile is the same gate on the hotter path.
//
// Every write takes a commit sequence, and every write is followed by a Fulltext
// catch-up. Both used to sweep: the sequence by taking the maximum over every Row
// and every Relation record in the Database, the catch-up by rebuilding the whole
// Catalog and every Route out of the record log. Row writes are unbounded in
// number, so this is where the cost compounded fastest.
func TestARowWriteNeverSweepsTheRecordFile(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)

	// The first Row also mounts a Route leaf, so both write paths are in.
	if _, err := rows.CreateTableRouterRoot(ctx, "work", table.Name, "Root", ""); err != nil {
		t.Fatal(err)
	}
	before := file.Enumerations()
	inserted, err := rows.Insert(ctx, "work", table.Name, map[string]any{
		table.Columns[0].Name: "swept?",
	}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
		Metadata:              row.WriteMetadata{Actor: "agent:test", Source: "msql", Reason: "insert"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if swept := file.Enumerations() - before; swept != 0 {
		t.Fatalf("one Row insert swept the record file %d times", swept)
	}

	before = file.Enumerations()
	if _, err := rows.Update(ctx, "work", table.Name, inserted.ID, map[string]any{
		table.Columns[0].Name: "still not swept",
	}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: inserted.Revision,
		Metadata: row.WriteMetadata{Actor: "agent:test", Source: "msql", Reason: "update"},
	}); err != nil {
		t.Fatal(err)
	}
	if swept := file.Enumerations() - before; swept != 0 {
		t.Fatalf("one Row update swept the record file %d times", swept)
	}
}
