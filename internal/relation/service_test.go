package relation_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestRelationStoreMaintainsRevisionedForwardAndReverseIndexes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.db")
	databaseStore, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service := relation.New(databaseStore, relation.Options{
		IDs:   &idSource{values: []string{"forward", "cycle"}},
		Clock: fixedClock{value: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)},
	})
	first := relation.Endpoint{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_first"}
	second := relation.Endpoint{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_second"}
	tx := mustBegin(t, ctx, databaseStore)
	forward, err := service.CreateIn(ctx, tx, relation.Definition{
		Source: first, Type: "depends_on", Target: second, Description: "First depends on second",
		CommitSequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := service.CreateIn(ctx, tx, relation.Definition{
		Source: second, Type: "depends_on", Target: first, Description: "Cycle is allowed",
		CommitSequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if forward.ID != "rel_forward" || forward.Revision != 1 || cycle.ID != "rel_cycle" {
		t.Fatalf("created relations = %#v, %#v", forward, cycle)
	}

	outgoing, err := service.ListOutgoing(ctx, first)
	if err != nil || len(outgoing) != 1 || outgoing[0].ID != forward.ID {
		t.Fatalf("ListOutgoing() = %#v, %v", outgoing, err)
	}
	incoming, err := service.ListIncoming(ctx, first)
	if err != nil || len(incoming) != 1 || incoming[0].ID != cycle.ID {
		t.Fatalf("ListIncoming() = %#v, %v", incoming, err)
	}

	tx = mustBegin(t, ctx, databaseStore)
	_, err = service.DeleteIn(ctx, tx, forward.ID, 2, 2)
	assertCode(t, err, result.CodeRevisionConflict)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	tx = mustBegin(t, ctx, databaseStore)
	deleted, err := service.DeleteIn(ctx, tx, forward.ID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if deleted.Revision != 2 || deleted.State != relation.StateDeleted || deleted.CommitSequence != 2 {
		t.Fatalf("deleted relation = %#v", deleted)
	}
	outgoing, err = service.ListOutgoing(ctx, first)
	if err != nil || len(outgoing) != 0 {
		t.Fatalf("outgoing after delete = %#v, %v", outgoing, err)
	}
	versions, err := service.History(ctx, forward.ID)
	if err != nil || len(versions) != 2 || versions[0].State != relation.StateLive ||
		versions[1].State != relation.StateDeleted {
		t.Fatalf("relation history = %#v, %v", versions, err)
	}

	duplicateService := relation.New(databaseStore, relation.Options{
		IDs: &idSource{values: []string{"cycle"}},
	})
	tx = mustBegin(t, ctx, databaseStore)
	_, err = duplicateService.CreateIn(ctx, tx, relation.Definition{
		Source: first, Type: "related_to", Target: second, CommitSequence: 3,
	})
	assertCode(t, err, result.CodeAlreadyExists)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := databaseStore.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedStore, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedStore.Close()
	reopened := relation.New(reopenedStore, relation.Options{})
	persisted, err := reopened.Get(ctx, cycle.ID)
	if err != nil || persisted.Revision != 1 || persisted.Source != second || persisted.Target != first {
		t.Fatalf("reopened relation = %#v, %v", persisted, err)
	}
}

func assertCode(t *testing.T, err error, want result.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %q", want)
	}
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) || stable.StableCode() != string(want) {
		t.Fatalf("error = %v, code = %q, want %q", err, stable.StableCode(), want)
	}
}

func TestRelationStoreRollbackLeavesNoRecordsOrIndexEntries(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service := relation.New(databaseStore, relation.Options{
		IDs: &idSource{values: []string{"rolled"}},
	})
	source := relation.Endpoint{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_first"}
	target := relation.Endpoint{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_second"}
	tx := mustBegin(t, ctx, databaseStore)
	created, err := service.CreateIn(ctx, tx, relation.Definition{
		Source: source, Type: "related_to", Target: target, CommitSequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(ctx, created.ID); err == nil {
		t.Fatal("rolled-back relation remained readable")
	}
	outgoing, err := service.ListOutgoing(ctx, source)
	if err != nil || len(outgoing) != 0 {
		t.Fatalf("rolled-back outgoing = %#v, %v", outgoing, err)
	}
	incoming, err := service.ListIncoming(ctx, target)
	if err != nil || len(incoming) != 0 {
		t.Fatalf("rolled-back incoming = %#v, %v", incoming, err)
	}
}

func mustBegin(t *testing.T, ctx context.Context, database store.Store) store.Tx {
	t.Helper()
	tx, err := database.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

type idSource struct {
	values []string
}

func (source *idSource) Next() (string, error) {
	value := source.values[0]
	source.values = source.values[1:]
	return value, nil
}

type fixedClock struct {
	value time.Time
}

func (clock fixedClock) Now() time.Time {
	return clock.value
}
