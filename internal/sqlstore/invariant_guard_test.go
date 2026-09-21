package sqlstore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
)

// The mount invariant has exactly one assertion site: the commit point that
// both autocommit and explicit transactions pass through. A path that would
// publish a live Row outside the one-to-one mount has to fail there, and the
// same Instance must keep committing ordinary writes.
func TestCommitRefusesAMountViolation(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "memora.db"), Options{CheckInvariants: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "p", Scope: "s"}); err != nil {
		t.Fatal(err)
	}
	table, err := db.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "p", RowSemantics: "one fact",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT", Purpose: "title", SemanticRole: "title"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := db.Rows().CreateTableRouterRoot(ctx, "work", "notes", "Everything", "")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := db.Rows().CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "leaf", Kind: router.KindLeaf, Purpose: "One fact",
	})
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := db.Rows().Insert(ctx, "work", "notes", map[string]any{"title": "kept"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, RouteLeafIDs: []string{leaf.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	// An ordinary write still goes through with the guard on.
	if err := db.update(ctx, func(t *tx) error {
		_, err := t.q().ExecContext(ctx,
			`UPDATE `+dataTable(table.ID)+` SET values_json = values_json WHERE row_id = ?`, inserted.ID)
		return err
	}); err != nil {
		t.Fatalf("a legal write must still commit: %v", err)
	}

	// A path that leaves the Row with no leaf must not reach COMMIT.
	err = db.update(ctx, func(t *tx) error {
		_, err := t.q().ExecContext(ctx,
			`UPDATE `+dataTable(table.ID)+` SET route_leaf_ids = '[]' WHERE row_id = ?`, inserted.ID)
		return err
	})
	if err == nil {
		t.Fatal("a transaction that empties the mount must not commit")
	}
	if !strings.Contains(err.Error(), "mount invariant violated") {
		t.Fatalf("error = %v", err)
	}

	// And the refused transaction left nothing behind.
	stored, err := db.Rows().Get(ctx, "work", "notes", inserted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.RouteLeafIDs) != 1 || stored.RouteLeafIDs[0] != leaf.ID {
		t.Fatalf("the rollback left the mount at %v", stored.RouteLeafIDs)
	}
}
