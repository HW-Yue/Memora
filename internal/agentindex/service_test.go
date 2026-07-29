package agentindex_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/agentindex"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestAgentIndexReplacesCompleteSnapshotAndInvalidatesOldRevision(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service := agentindex.New(databaseStore, agentindex.Options{})
	if service.TargetTerms() != 24 || service.MaxTerms() != 64 {
		t.Fatalf("budgets = %d/%d", service.TargetTerms(), service.MaxTerms())
	}
	locator := agentindex.Locator{
		DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_note", Revision: 1,
	}

	tx := mustBegin(t, ctx, databaseStore)
	snapshot, err := service.ReplaceIn(ctx, tx, locator, []string{
		"数据库", "Architecture", " architecture ", "from   body", "FROM BODY",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Terms) != 3 || snapshot.Terms[0] != "architecture" ||
		snapshot.Terms[1] != "from body" || snapshot.Terms[2] != "数据库" {
		t.Fatalf("normalized snapshot = %#v", snapshot)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertLookup(t, ctx, service, "db_work", "ARCHITECTURE", "row_note", 1)

	tx = mustBegin(t, ctx, databaseStore)
	replaced, err := service.ReplaceIn(ctx, tx, agentindex.Locator{
		DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_note", Revision: 2,
	}, []string{"new term"})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Revision != 2 || len(replaced.Terms) != 1 {
		t.Fatalf("replacement = %#v", replaced)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if matches, err := service.Lookup(ctx, "db_work", "architecture", 10); err != nil || len(matches) != 0 {
		t.Fatalf("old revision lookup = %#v, %v", matches, err)
	}
	assertLookup(t, ctx, service, "db_work", "new term", "row_note", 2)

	tx = mustBegin(t, ctx, databaseStore)
	if _, err := service.ReplaceIn(ctx, tx, agentindex.Locator{
		DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_note", Revision: 3,
	}, []string{"rolled back"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertLookup(t, ctx, service, "db_work", "new term", "row_note", 2)
	if matches, err := service.Lookup(ctx, "db_work", "rolled back", 10); err != nil || len(matches) != 0 {
		t.Fatalf("rolled-back lookup = %#v, %v", matches, err)
	}
}

func TestAgentIndexEnforcesBudgetRevisionAndLogicalInvalidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service := agentindex.New(databaseStore, agentindex.Options{})
	locator := agentindex.Locator{
		DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_note", Revision: 1,
	}
	tooMany := make([]string, 65)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("term %d", index)
	}
	tx := mustBegin(t, ctx, databaseStore)
	_, err = service.ReplaceIn(ctx, tx, locator, tooMany)
	assertCode(t, err, result.CodeConstraint)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	tx = mustBegin(t, ctx, databaseStore)
	if _, err := service.ReplaceIn(ctx, tx, locator, []string{"active"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx = mustBegin(t, ctx, databaseStore)
	_, err = service.ReplaceIn(ctx, tx, locator, []string{"stale"})
	assertCode(t, err, result.CodeRevisionConflict)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	tx = mustBegin(t, ctx, databaseStore)
	invalidated, err := service.InvalidateIn(ctx, tx, agentindex.Locator{
		DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_note", Revision: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if invalidated.State != agentindex.StateInvalid || len(invalidated.Terms) != 0 {
		t.Fatalf("invalidated snapshot = %#v", invalidated)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if matches, err := service.Lookup(ctx, "db_work", "active", 10); err != nil || len(matches) != 0 {
		t.Fatalf("invalidated lookup = %#v, %v", matches, err)
	}
}

func assertLookup(
	t *testing.T,
	ctx context.Context,
	service *agentindex.Service,
	databaseID, term, rowID string,
	revision uint64,
) {
	t.Helper()
	matches, err := service.Lookup(ctx, databaseID, term, 10)
	if err != nil || len(matches) != 1 || matches[0].RowID != rowID ||
		matches[0].Revision != revision || matches[0].Source != agentindex.SourceAgent {
		t.Fatalf("Lookup(%q) = %#v, %v", term, matches, err)
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

func assertCode(t *testing.T, err error, want result.Code) {
	t.Helper()
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) || stable.StableCode() != string(want) {
		t.Fatalf("error = %v, want code %q", err, want)
	}
}
