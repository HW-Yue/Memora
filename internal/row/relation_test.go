package row_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestRelationIntegritySupportsCrossTableCyclesAndDeleteCascade(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{
			"work", "notes", "note_title", "tasks", "task_title",
			"personal", "people", "person_name",
		}},
	})
	createRelationSchema(t, ctx, dictionary)
	service := row.New(databaseStore, dictionary, row.Options{
		IDs:         &idSource{values: []string{"note", "task", "person"}},
		RelationIDs: &relationIDSource{values: []string{"forward", "cycle", "cross"}},
	})

	note := mustInsert(t, ctx, service, "work", "notes", "title", "note")
	task := mustInsert(t, ctx, service, "work", "tasks", "title", "task")
	person := mustInsert(t, ctx, service, "personal", "people", "name", "person")

	tx, err := service.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	missing := row.RelationEndpoint{Database: "work", Table: "tasks", RowID: "row_missing"}
	_, err = tx.Relate(ctx, row.RelationDefinition{
		Source: endpoint("work", "notes", note.ID), Type: "depends_on", Target: missing,
	})
	assertCode(t, err, result.CodeNotFound)

	forward, err := tx.Relate(ctx, row.RelationDefinition{
		Source: endpoint("work", "notes", note.ID), Type: "depends_on",
		Target: endpoint("work", "tasks", task.ID), Description: "cross-table edge",
	})
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := tx.Relate(ctx, row.RelationDefinition{
		Source: endpoint("work", "tasks", task.ID), Type: "supports",
		Target: endpoint("work", "notes", note.ID), Description: "cycles are valid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if forward.CommitSequence != cycle.CommitSequence {
		t.Fatalf("relationship commit sequences = %d, %d", forward.CommitSequence, cycle.CommitSequence)
	}
	_, err = tx.Relate(ctx, row.RelationDefinition{
		Source: endpoint("work", "notes", note.ID), Type: "knows",
		Target: endpoint("personal", "people", person.ID),
	})
	assertCode(t, err, result.CodeConstraint)
	if _, err := tx.Update(ctx, "work", "tasks", task.ID, map[string]any{
		"title": "updated task",
	}, row.WriteOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	taskIncoming, err := tx.ListIncomingRelations(ctx, endpoint("work", "tasks", task.ID))
	if err != nil || len(taskIncoming) != 1 || taskIncoming[0].ID != forward.ID {
		t.Fatalf("relations after ordinary row update = %#v, %v", taskIncoming, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	tx, err = service.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	outgoing, err := tx.ListOutgoingRelations(ctx, endpoint("work", "notes", note.ID))
	if err != nil || len(outgoing) != 1 || outgoing[0].ID != forward.ID {
		t.Fatalf("outgoing = %#v, %v", outgoing, err)
	}
	incoming, err := tx.ListIncomingRelations(ctx, endpoint("work", "notes", note.ID))
	if err != nil || len(incoming) != 1 || incoming[0].ID != cycle.ID {
		t.Fatalf("incoming = %#v, %v", incoming, err)
	}
	deletedRow, err := tx.Delete(ctx, "work", "notes", note.ID, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	outgoing, err = tx.ListOutgoingRelations(ctx, endpoint("work", "notes", note.ID))
	if err != nil || len(outgoing) != 0 {
		t.Fatalf("outgoing after delete = %#v, %v", outgoing, err)
	}
	incoming, err = tx.ListIncomingRelations(ctx, endpoint("work", "notes", note.ID))
	if err != nil || len(incoming) != 0 {
		t.Fatalf("incoming after delete = %#v, %v", incoming, err)
	}
	deletedForward, err := tx.GetRelation(ctx, forward.ID)
	if err != nil || deletedForward.State != relation.StateDeleted ||
		deletedForward.CommitSequence != deletedRow.CommitSequence {
		t.Fatalf("deleted forward = %#v, %v", deletedForward, err)
	}
	deletedCycle, err := tx.GetRelation(ctx, cycle.ID)
	if err != nil || deletedCycle.State != relation.StateDeleted ||
		deletedCycle.CommitSequence != deletedRow.CommitSequence {
		t.Fatalf("deleted cycle = %#v, %v", deletedCycle, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestRelationPolicyCanAuthorizeCrossDatabaseAndRollbackIsAtomic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{
			"work", "notes", "note_title", "tasks", "task_title",
			"personal", "people", "person_name",
		}},
	})
	createRelationSchema(t, ctx, dictionary)
	service := row.New(databaseStore, dictionary, row.Options{
		IDs:         &idSource{values: []string{"note", "person"}},
		RelationIDs: &relationIDSource{values: []string{"cross"}},
		RelationPolicy: row.RelationPolicyFunc(func(
			_ context.Context, source, target catalog.Table,
		) bool {
			return source.DatabaseID != target.DatabaseID
		}),
	})
	note := mustInsert(t, ctx, service, "work", "notes", "title", "note")
	person := mustInsert(t, ctx, service, "personal", "people", "name", "person")

	tx, err := service.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	created, err := tx.Relate(ctx, row.RelationDefinition{
		Source: endpoint("work", "notes", note.ID), Type: "knows",
		Target: endpoint("personal", "people", person.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	tx, err = service.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.GetRelation(ctx, created.ID); err == nil {
		t.Fatal("rolled-back relation remained visible")
	}
	outgoing, err := tx.ListOutgoingRelations(ctx, endpoint("work", "notes", note.ID))
	if err != nil || len(outgoing) != 0 {
		t.Fatalf("rolled-back outgoing = %#v, %v", outgoing, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func endpoint(database, table, rowID string) row.RelationEndpoint {
	return row.RelationEndpoint{Database: database, Table: table, RowID: rowID}
}

func mustInsert(
	t *testing.T,
	ctx context.Context,
	service *row.Service,
	database, table, column, value string,
) row.Row {
	t.Helper()
	inserted, err := service.Insert(ctx, database, table, map[string]any{column: value}, row.WriteOptions{
		ExpectedSchemaVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return inserted
}

func createRelationSchema(t *testing.T, ctx context.Context, dictionary *catalog.Service) {
	t.Helper()
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Work knowledge",
	}); err != nil {
		t.Fatal(err)
	}
	for _, definition := range []catalog.TableDefinition{
		{
			Name: "notes", Purpose: "Notes", Scope: "Notes", RowSemantics: "One note",
			Columns: []catalog.ColumnDefinition{
				{Name: "title", Type: "text", Purpose: "Title"},
			},
		},
		{
			Name: "tasks", Purpose: "Tasks", Scope: "Tasks", RowSemantics: "One task",
			Columns: []catalog.ColumnDefinition{
				{Name: "title", Type: "text", Purpose: "Title"},
			},
		},
	} {
		if _, err := dictionary.CreateTable(ctx, "work", definition); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "personal", Purpose: "Personal", Scope: "Personal knowledge",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "personal", catalog.TableDefinition{
		Name: "people", Purpose: "People", Scope: "People", RowSemantics: "One person",
		Columns: []catalog.ColumnDefinition{
			{Name: "name", Type: "text", Purpose: "Name"},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

type relationIDSource struct {
	values []string
}

func (source *relationIDSource) Next() (string, error) {
	value := source.values[0]
	source.values = source.values[1:]
	return value, nil
}
