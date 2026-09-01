package pagestoremigration

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/row"
)

// TestRelationReadsNeverSweepTheRecordFile is E7 stage 3's gate for Relations.
//
// Relations were the worst of the resident-map readers, not merely one of them:
// ListRelations swept the file to collect the logical IDs and then swept again
// inside GetRelation for each one, so listing an endpoint's links cost a pass
// over every Relation record ever written, squared.
//
// They were also the only core object that never reached the Authority at all —
// commitRelationChange wrote straight to the record log — so the generation had
// never heard of them and the resident map was the only index they had.
func TestRelationReadsNeverSweepTheRecordFile(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	if _, err := rows.CreateTableRouterRoot(ctx, "work", table.Name, "Root", ""); err != nil {
		t.Fatal(err)
	}
	first := insertScanRow(t, ctx, rows, table, "first")
	second := insertScanRow(t, ctx, rows, table, "second")

	before := file.Enumerations()
	created, err := rows.Relate(ctx, row.RelationDefinition{
		Source:      row.RelationEndpoint{Database: "work", Table: table.Name, RowID: first.ID},
		Type:        "supports",
		Target:      row.RelationEndpoint{Database: "work", Table: table.Name, RowID: second.ID},
		Description: "first supports second",
	})
	if err != nil {
		t.Fatal(err)
	}
	if swept := file.Enumerations() - before; swept != 0 {
		t.Fatalf("creating one Relation swept the record file %d times", swept)
	}

	before = file.Enumerations()
	stored, err := rows.GetRelation(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := rows.ListOutgoingRelations(ctx, row.RelationEndpoint{
		Database: "work", Table: table.Name, RowID: first.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if swept := file.Enumerations() - before; swept != 0 {
		t.Fatalf("reading Relations swept the record file %d times", swept)
	}
	if stored.ID != created.ID || stored.Revision != created.Revision ||
		stored.Type != "supports" || stored.Description != created.Description {
		t.Fatalf("GetRelation through the Tree = %#v, want %#v", stored, created)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("ListOutgoingRelations through the Tree = %#v", listed)
	}

	// A deleted Relation is unreachable from the live read surface and its
	// tombstone is what the Tree holds — "it is gone" is an answer, not silence.
	before = file.Enumerations()
	if _, err := rows.DeleteRelation(ctx, created.ID, created.Revision); err != nil {
		t.Fatal(err)
	}
	if swept := file.Enumerations() - before; swept != 0 {
		t.Fatalf("deleting one Relation swept the record file %d times", swept)
	}
	if _, err := rows.GetRelation(ctx, created.ID); err == nil {
		t.Fatal("a deleted Relation is still readable")
	}
	tombstone, err := authority.Generation().Objects().Lookup(nativerow.RelationObjectKind, created.ID)
	if err != nil {
		t.Fatalf("the tombstone is not in the objects Tree: %v", err)
	}
	if tombstone.Revision != created.Revision+1 {
		t.Fatalf("tombstone revision = %d, want %d", tombstone.Revision, created.Revision+1)
	}
}

func insertScanRow(
	t *testing.T, ctx context.Context, rows *nativerow.Service, table catalog.Table, title string,
) row.Row {
	t.Helper()
	value, err := rows.Insert(ctx, "work", table.Name, map[string]any{
		table.Columns[0].Name: title,
	}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
		Metadata:              row.WriteMetadata{Actor: "agent:test", Source: "msql", Reason: "insert"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
