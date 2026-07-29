package mechanicalindex_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/agentindex"
	"github.com/HW-Yue/Memora/internal/mechanicalindex"
	"github.com/HW-Yue/Memora/internal/store"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestMechanicalIndexTokenizesMixedTextAndRebuildsSameRevision(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service := mechanicalindex.New(databaseStore, mechanicalindex.Options{})
	locator := mechanicalindex.Locator{
		DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_note", Revision: 1,
	}
	tx := mustBegin(t, ctx, databaseStore)
	snapshot, err := service.ReplaceIn(ctx, tx, locator, []string{
		"Memora，数据库架构-design!",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range []string{"memora", "mem", "emo", "数据", "据库", "库架", "架构", "design"} {
		if !contains(snapshot.Terms, term) {
			t.Fatalf("terms = %#v, missing %q", snapshot.Terms, term)
		}
	}
	if snapshot.Source != mechanicalindex.SourceMechanical || snapshot.Truncated {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertLookup(t, ctx, service, "db_work", "数据", "row_note", 1)

	tx = mustBegin(t, ctx, databaseStore)
	rebuilt, err := service.RebuildIn(ctx, tx, locator, []string{"重建后的内容"})
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Revision != 1 || !contains(rebuilt.Terms, "重建") {
		t.Fatalf("rebuilt = %#v", rebuilt)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if matches, err := service.Lookup(ctx, "db_work", "数据", 10); err != nil || len(matches) != 0 {
		t.Fatalf("old rebuild term = %#v, %v", matches, err)
	}
	assertLookup(t, ctx, service, "db_work", "重建", "row_note", 1)
}

func TestMechanicalIndexHonorsSpaceBudgetDisableRollbackAndAgentIsolation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	mechanical := mechanicalindex.New(databaseStore, mechanicalindex.Options{
		MaxTerms: 8, MaxInputCharacters: 80,
	})
	agent := agentindex.New(databaseStore, agentindex.Options{})
	locator := mechanicalindex.Locator{
		DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_note", Revision: 1,
	}
	tx := mustBegin(t, ctx, databaseStore)
	if _, err := agent.ReplaceIn(ctx, tx, agentindex.Locator(locator), []string{"shared"}); err != nil {
		t.Fatal(err)
	}
	longText := strings.Repeat("alpha beta gamma delta epsilon zeta eta theta iota ", 20)
	snapshot, err := mechanical.ReplaceIn(ctx, tx, locator, []string{longText, "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Truncated || len(snapshot.Terms) != 8 {
		t.Fatalf("budgeted snapshot = %#v", snapshot)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	tx = mustBegin(t, ctx, databaseStore)
	if _, err := mechanical.InvalidateIn(ctx, tx, mechanicalindex.Locator{
		DatabaseID: locator.DatabaseID, TableID: locator.TableID,
		RowID: locator.RowID, Revision: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if matches, err := agent.Lookup(ctx, "db_work", "shared", 10); err != nil || len(matches) != 1 {
		t.Fatalf("Agent posting after mechanical rollback = %#v, %v", matches, err)
	}

	disabled := mechanicalindex.New(databaseStore, mechanicalindex.Options{Disabled: true})
	tx = mustBegin(t, ctx, databaseStore)
	disabledSnapshot, err := disabled.ReplaceIn(ctx, tx, mechanicalindex.Locator{
		DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_disabled", Revision: 1,
	}, []string{"must not be posted"})
	if err != nil {
		t.Fatal(err)
	}
	if disabledSnapshot.State != mechanicalindex.StateDisabled || len(disabledSnapshot.Terms) != 0 {
		t.Fatalf("disabled snapshot = %#v", disabledSnapshot)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if matches, err := disabled.Lookup(ctx, "db_work", "must", 10); err != nil || len(matches) != 0 {
		t.Fatalf("disabled lookup = %#v, %v", matches, err)
	}
}

func assertLookup(
	t *testing.T,
	ctx context.Context,
	service *mechanicalindex.Service,
	databaseID, term, rowID string,
	revision uint64,
) {
	t.Helper()
	matches, err := service.Lookup(ctx, databaseID, term, 10)
	if err != nil || len(matches) != 1 || matches[0].RowID != rowID ||
		matches[0].Revision != revision ||
		matches[0].Source != mechanicalindex.SourceMechanical {
		t.Fatalf("Lookup(%q) = %#v, %v", term, matches, err)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func mustBegin(t *testing.T, ctx context.Context, database store.Store) store.Tx {
	t.Helper()
	tx, err := database.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}
