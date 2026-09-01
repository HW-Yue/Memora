package pagestoremigration

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
)

// TestALiveWorkloadNeverSweepsTheRecordFile is the summary gate for architecture
// principle four: once a Database is open, nothing it serves may enumerate the
// record log.
//
// Opening still sweeps — the generation is derived, and building it from the
// record log is exactly what a full read is for. What must never sweep again is
// everything after that. Enumerations() counts sweeps, so this asserts the
// counter does not move across a mixed read and write workload.
//
// The remaining IDs() call sites are all outside this: the no-generation
// fallbacks, the migration Reader that builds the Plan, and the snapshot export.
func TestALiveWorkloadNeverSweepsTheRecordFile(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	root, err := rows.CreateTableRouterRoot(ctx, "work", table.Name, "Root", "")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := rows.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "leaf", Kind: router.KindLeaf, Purpose: "Leaf",
	})
	if err != nil {
		t.Fatal(err)
	}

	before := file.Enumerations()
	first, err := rows.Insert(ctx, "work", table.Name, map[string]any{
		table.Columns[0].Name: "first",
	}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, RouteLeafIDs: []string{leaf.ID},
		Metadata: row.WriteMetadata{Actor: "agent:test", Source: "msql", Reason: "insert"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := rows.Insert(ctx, "work", table.Name, map[string]any{
		table.Columns[0].Name: "second",
	}, row.WriteOptions{ExpectedSchemaVersion: table.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Update(ctx, "work", table.Name, first.ID, map[string]any{
		table.Columns[0].Name: "revised",
	}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: first.Revision,
		Metadata: row.WriteMetadata{Actor: "agent:test", Source: "msql", Reason: "update"},
	}); err != nil {
		t.Fatal(err)
	}
	related, err := rows.Relate(ctx, row.RelationDefinition{
		Source:      row.RelationEndpoint{Database: "work", Table: table.Name, RowID: first.ID},
		Type:        "supports",
		Target:      row.RelationEndpoint{Database: "work", Table: table.Name, RowID: second.ID},
		Description: "first supports second",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Reads: every surface a session actually uses.
	if _, err := rows.Get(ctx, "work", table.Name, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rows.ListPage(ctx, "work", table.Name, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := rows.AsOfRevision(ctx, "work", table.Name, first.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rows.HistoryPage(ctx, "work", table.Name, first.ID, "", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := rows.GetRelation(ctx, related.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rows.ListOutgoingRelations(ctx, row.RelationEndpoint{
		Database: "work", Table: table.Name, RowID: first.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rows.ListRouterChildren(ctx, root.ID, "", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.DescribeTable(ctx, "work", table.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.SnapshotCatalog(ctx); err != nil {
		t.Fatal(err)
	}

	if swept := file.Enumerations() - before; swept != 0 {
		t.Fatalf("a live workload swept the record file %d times", swept)
	}
}
