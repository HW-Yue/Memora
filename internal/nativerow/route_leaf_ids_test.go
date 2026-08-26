package nativerow

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// TestRowCarriesItsRouteLeafIDs is E3 stage 2's gate.
//
// "Which leaves is this Row hanging under" is needed on three live paths — the
// route_paths of every SELECT, leaf cleanup on delete, and SPLIT/MERGE
// remounting. It used to be answered by scanning Membership objects.
//
// It does not need answering: the write order already knows. A Row is written
// after its RowID has been mounted on the leaves, so RouteLeafIDs is in hand at
// the moment the Row is encoded. Storing what you already know beats building a
// third structure to look it up. See docs/storage/leaf-rowid-v1.md §5.
func TestRowCarriesItsRouteLeafIDs(t *testing.T) {
	table := catalog.Table{
		ID: "tbl_notes", DatabaseID: "db_work", SchemaVersion: 1,
		Columns: []catalog.Column{{ID: "col_title", Type: "TEXT"}},
	}
	written := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	value := row.Row{
		ID: "row_mounted", DatabaseID: "db_work", TableID: "tbl_notes",
		SchemaVersion: 1, Revision: 1, CommitSequence: 1, State: row.StateLive,
		Values: map[string]any{"col_title": "mounted"}, CreatedAt: written, UpdatedAt: written,
		RouteLeafIDs: []string{"route_alpha", "route_beta"},
	}
	encoded, err := encode(value, table)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.RouteLeafIDs) != 2 ||
		decoded.RouteLeafIDs[0] != "route_alpha" || decoded.RouteLeafIDs[1] != "route_beta" {
		t.Fatalf("decoded RouteLeafIDs = %#v", decoded.RouteLeafIDs)
	}
	if decoded.ID != value.ID || decoded.Values["col_title"] != "mounted" {
		t.Fatalf("round trip lost an existing field: %#v", decoded)
	}
}

// TestRowWithoutRouteLeafIDsStillDecodes keeps the field additive.
//
// The record format already grows this way: ChangeSequence is a flag bit plus a
// trailer, and a record written before it simply does not set the bit. A Row
// that predates the leaf list must read back unchanged, with no leaves rather
// than a decode failure.
func TestRowWithoutRouteLeafIDsStillDecodes(t *testing.T) {
	table := catalog.Table{
		ID: "tbl_notes", DatabaseID: "db_work", SchemaVersion: 1,
		Columns: []catalog.Column{{ID: "col_title", Type: "TEXT"}},
	}
	written := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	value := row.Row{
		ID: "row_legacy", DatabaseID: "db_work", TableID: "tbl_notes",
		SchemaVersion: 1, Revision: 1, CommitSequence: 1, State: row.StateLive,
		Values: map[string]any{"col_title": "legacy"}, CreatedAt: written, UpdatedAt: written,
	}
	encoded, err := encode(value, table)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decode(encoded)
	if err != nil {
		t.Fatalf("a Row written without leaves no longer decodes: %v", err)
	}
	if len(decoded.RouteLeafIDs) != 0 {
		t.Fatalf("decoded RouteLeafIDs = %#v, want none", decoded.RouteLeafIDs)
	}
	if decoded.ID != "row_legacy" || decoded.Values["col_title"] != "legacy" {
		t.Fatalf("old record decoded to %#v", decoded)
	}
}

// TestRowAndLeafAgreeOnTheMount replaces the dual-write parity gate.
//
// Memberships are gone, so there is no second source to compare against. What
// still has to hold is that the two halves of the mount agree with each other:
// the Row names the leaves, and each of those leaves names the Row back. They
// are written in one transaction, so a disagreement means the write path put
// them out of step.
func TestRowAndLeafAgreeOnTheMount(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs: &testIDs{values: []string{"database", "table", "title"}}, Clock: testClock{value: now},
	})
	rows := NewService(New(file), dictionary, ServiceOptions{
		IDs:   &testIDs{values: []string{"root", "alpha", "beta", "gamma", "mounted"}},
		Clock: testClock{value: now},
	})
	engine := executor.New(dictionary, rows)
	executeMSQL(t, ctx, engine, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(40) NOT NULL PURPOSE 'Title')", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'", executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
	for _, name := range []string{"alpha", "beta", "gamma"} {
		executeMSQL(t, ctx, engine,
			"CREATE ROUTE UNDER 'route_root' NAME :name KIND 'leaf' PURPOSE :purpose",
			executor.Parameters{Named: map[string]any{"name": name, "purpose": name + " locator"}},
			executor.MutationOptions{MaxAffectedRows: 1})
	}

	// Named out of order on purpose: the stored list is a function of which
	// leaves, not of the order the caller listed them.
	executeMSQL(t, ctx, engine, "INSERT INTO work.notes (title) VALUES ('mounted')",
		executor.Parameters{}, executor.MutationOptions{
			ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
			RouteLeafIDs: []string{"route_beta", "route_alpha"},
		})
	assertMountAgrees(t, file, rows, ctx, "row_mounted", []string{"route_alpha", "route_beta"})

	// A remount has to move both halves together, including the leaf dropped.
	executeMSQL(t, ctx, engine, "UPDATE work.notes SET title = 'remounted' WHERE row_id = 'row_mounted'",
		executor.Parameters{}, executor.MutationOptions{
			ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
			RouteLeafIDs: []string{"route_gamma", "route_alpha"},
		})
	assertMountAgrees(t, file, rows, ctx, "row_mounted", []string{"route_alpha", "route_gamma"})
}

func assertMountAgrees(
	t *testing.T, file *nativestore.File, rows *Service, ctx context.Context,
	rowID string, want []string,
) {
	t.Helper()
	stored, err := rows.Get(ctx, "work", "notes", rowID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(stored.RouteLeafIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("Row leaf list = %#v, want %#v", stored.RouteLeafIDs, want)
	}
	holding, err := nativerouter.New(file).LeavesHoldingRow(rowID)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(holding)
	if strings.Join(holding, ",") != strings.Join(want, ",") {
		t.Fatalf("leaves naming the Row = %#v, want %#v", holding, want)
	}
}

// TestLeafNamesTheRowItHolds is the other direction of the mount.
//
// The Row stores its leaves; each leaf stores its Row. Both land in the same
// transaction, which is what makes storing the fact twice safe — and what
// separates this from Membership, an independent object with its own revision
// and tombstone that could drift from both ends.
func TestLeafNamesTheRowItHolds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs: &testIDs{values: []string{"database", "table", "title"}}, Clock: testClock{value: now},
	})
	rows := NewService(New(file), dictionary, ServiceOptions{
		IDs:   &testIDs{values: []string{"root", "here", "there", "held"}},
		Clock: testClock{value: now},
	})
	engine := executor.New(dictionary, rows)
	executeMSQL(t, ctx, engine, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(40) NOT NULL PURPOSE 'Title')", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'", executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
	for _, name := range []string{"here", "there"} {
		executeMSQL(t, ctx, engine,
			"CREATE ROUTE UNDER 'route_root' NAME :name KIND 'leaf' PURPOSE :purpose",
			executor.Parameters{Named: map[string]any{"name": name, "purpose": name + " locator"}},
			executor.MutationOptions{MaxAffectedRows: 1})
	}
	routes := nativerouter.New(file)

	executeMSQL(t, ctx, engine, "INSERT INTO work.notes (title) VALUES ('held')",
		executor.Parameters{}, executor.MutationOptions{
			ExpectedSchemaVersion: 1, MaxAffectedRows: 1, RouteLeafIDs: []string{"route_here"},
		})
	assertLeafHolds(t, routes, "route_here", "row_held")
	assertLeafHolds(t, routes, "route_there", "")

	// Moving the Row has to clear the leaf it left, not only fill the new one.
	// A leaf still naming a Row that moved away is precisely the stale pointer
	// Membership could produce and this design is meant to make impossible.
	executeMSQL(t, ctx, engine, "UPDATE work.notes SET title = 'moved' WHERE row_id = 'row_held'",
		executor.Parameters{}, executor.MutationOptions{
			ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
			RouteLeafIDs: []string{"route_there"},
		})
	assertLeafHolds(t, routes, "route_here", "")
	assertLeafHolds(t, routes, "route_there", "row_held")
}

func assertLeafHolds(t *testing.T, routes *nativerouter.Repository, leafID, want string) {
	t.Helper()
	node, err := routes.Get(leafID)
	if err != nil {
		t.Fatal(err)
	}
	if node.RowID != want {
		t.Fatalf("leaf %s holds %q, want %q", leafID, node.RowID, want)
	}
}

// TestOpenRouteReadsTheLeafField pins where OPEN gets its answer.
//
// Nothing writes Membership objects any more, so the earlier version of this
// test — make the two sources disagree and see which one wins — has no second
// source left to disagree with. What it still proves is that emptying the leaf
// field empties the OPEN result, which is the whole of the read path now.
func TestOpenRouteReadsTheLeafField(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs: &testIDs{values: []string{"database", "table", "title"}}, Clock: testClock{value: now},
	})
	rows := NewService(New(file), dictionary, ServiceOptions{
		IDs:   &testIDs{values: []string{"root", "only", "held"}},
		Clock: testClock{value: now},
	})
	engine := executor.New(dictionary, rows)
	executeMSQL(t, ctx, engine, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(40) NOT NULL PURPOSE 'Title')", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'", executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
	executeMSQL(t, ctx, engine, "CREATE ROUTE UNDER 'route_root' NAME 'only' KIND 'leaf' PURPOSE 'The one leaf'",
		executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
	executeMSQL(t, ctx, engine, "INSERT INTO work.notes (title) VALUES ('held')",
		executor.Parameters{}, executor.MutationOptions{
			ExpectedSchemaVersion: 1, MaxAffectedRows: 1, RouteLeafIDs: []string{"route_only"},
		})

	locators, _, err := rows.ListRouterLeafPage(ctx, "route_only", "", 10)
	if err != nil || len(locators) != 1 || locators[0].RowID != "row_held" || locators[0].Revision != 1 {
		t.Fatalf("OPEN on a mounted leaf = %#v, %v", locators, err)
	}

	routes := nativerouter.New(file)
	leaf, err := routes.Get("route_only")
	if err != nil {
		t.Fatal(err)
	}
	leaf.RowID, leaf.Revision = "", leaf.Revision+1
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := routes.StageNode(transaction, leaf); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	locators, _, err = rows.ListRouterLeafPage(ctx, "route_only", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(locators) != 0 {
		t.Fatalf("OPEN on an emptied leaf = %#v", locators)
	}
}

// TestMountingAnOccupiedLeafIsRefused is where the single-Row invariant lives
// now.
//
// It used to be checked by counting live Memberships per leaf, which could only
// notice an ambiguous leaf after one had been created. A field holds one value,
// so the second Row is refused at the write instead.
func TestMountingAnOccupiedLeafIsRefused(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs: &testIDs{values: []string{"database", "table", "title"}}, Clock: testClock{value: now},
	})
	rows := NewService(New(file), dictionary, ServiceOptions{
		IDs:   &testIDs{values: []string{"root", "only", "first", "second"}},
		Clock: testClock{value: now},
	})
	engine := executor.New(dictionary, rows)
	executeMSQL(t, ctx, engine, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(40) NOT NULL PURPOSE 'Title')", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'", executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
	executeMSQL(t, ctx, engine, "CREATE ROUTE UNDER 'route_root' NAME 'only' KIND 'leaf' PURPOSE 'The one leaf'",
		executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
	executeMSQL(t, ctx, engine, "INSERT INTO work.notes (title) VALUES ('first')",
		executor.Parameters{}, executor.MutationOptions{
			ExpectedSchemaVersion: 1, MaxAffectedRows: 1, RouteLeafIDs: []string{"route_only"},
		})

	_, err = runMSQL(ctx, engine, "INSERT INTO work.notes (title) VALUES ('second')",
		executor.Parameters{}, executor.MutationOptions{
			ExpectedSchemaVersion: 1, MaxAffectedRows: 1, RouteLeafIDs: []string{"route_only"},
		})
	if !hasCode(err, string(result.CodeConstraint)) {
		t.Fatalf("second Row into an occupied leaf = %v, want a constraint refusal", err)
	}
	// The refusal leaves the first Row exactly where it was.
	assertLeafHolds(t, nativerouter.New(file), "route_only", "row_first")
}
