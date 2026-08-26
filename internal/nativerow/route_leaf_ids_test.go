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
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// TestRowCarriesItsRouteLeafIDs is E3 stage 2's gate.
//
// "Which leaves is this Row hanging under" is needed on three live paths — the
// route_paths of every SELECT, leaf cleanup on delete, and SPLIT/MERGE
// remounting. Today it is answered by scanning Membership objects.
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

// TestRowLeafListAgreesWithMemberships is the parity gate for the dual write.
//
// Two sources now answer "which leaves is this Row under": the Membership
// objects that have always answered it, and the Row's own field. While both
// exist they must agree exactly — that is what makes the read switch in the
// next stage a no-op rather than a change of answer.
func TestRowLeafListAgreesWithMemberships(t *testing.T) {
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
	assertLeafParity(t, file, rows, ctx, "row_mounted", []string{"route_alpha", "route_beta"})

	// A remount has to move both sources together, including the leaf dropped.
	executeMSQL(t, ctx, engine, "UPDATE work.notes SET title = 'remounted' WHERE row_id = 'row_mounted'",
		executor.Parameters{}, executor.MutationOptions{
			ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
			RouteLeafIDs: []string{"route_gamma", "route_alpha"},
		})
	assertLeafParity(t, file, rows, ctx, "row_mounted", []string{"route_alpha", "route_gamma"})
}

func assertLeafParity(
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
	memberships, err := nativerouter.New(file).MembershipsIncludingDeleted(rowID)
	if err != nil {
		t.Fatal(err)
	}
	live := make([]string, 0, len(memberships))
	for _, membership := range memberships {
		if !membership.Deleted {
			live = append(live, membership.LeafID)
		}
	}
	sort.Strings(live)
	if strings.Join(live, ",") != strings.Join(want, ",") {
		t.Fatalf("Membership leaf list = %#v, want %#v", live, want)
	}
}
