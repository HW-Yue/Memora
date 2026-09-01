package nativemutation

import (
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativechange"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestCrossObjectMutationPublishesAtomicallyAfterReopen(t *testing.T) {
	t.Parallel()

	path, file, rows, routes, current, edge, leafID := mutationFixture(t)
	plan := revisedPlan(current, edge)
	if err := New(file, rows, routes).Commit(plan); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	rows, routes = nativerow.New(reopened), nativerouter.New(reopened)
	latest, err := rows.Read(current.ID)
	if err != nil || latest.Revision != 2 || latest.Values["col_title"] != "revised" {
		t.Fatalf("latest Row = %#v, %v", latest, err)
	}
	records, more, err := rows.History(current.DatabaseID, current.TableID, current.ID, 10)
	if err != nil || more || len(records) != 2 || records[0].Operation != history.OperationUpdate {
		t.Fatalf("History() = %#v, %v, %v", records, more, err)
	}
	if _, err := rows.GetRelation(edge.ID, false); !errors.Is(err, nativestore.ErrNotFound) {
		t.Fatalf("GetRelation(live) error = %v", err)
	}
	deleted, err := rows.GetRelation(edge.ID, true)
	if err != nil || deleted.Revision != 2 || deleted.State != relation.StateDeleted {
		t.Fatalf("deleted Relation = %#v, %v", deleted, err)
	}
	locators := openLeaf(t, routes, rows, leafID)
	if len(locators) != 1 || locators[0].Revision != 2 {
		t.Fatalf("leaf locators = %#v", locators)
	}
	// Both ends of the mount survive the reopen, and they agree.
	leaf, err := routes.Get(leafID)
	if err != nil || leaf.RowID != current.ID {
		t.Fatalf("mounted leaf = %#v, %v", leaf, err)
	}
	if len(latest.RouteLeafIDs) != 1 || latest.RouteLeafIDs[0] != leafID {
		t.Fatalf("Row leaf list = %#v", latest.RouteLeafIDs)
	}
	changes, more, err := nativechange.New(reopened).ListAfter(0, 10)
	if err != nil || more || len(changes) != 1 || len(changes[0].Entries) != 2 ||
		countEntries(changes[0], change.ObjectRow) != 1 ||
		countEntries(changes[0], change.ObjectRelation) != 1 {
		t.Fatalf("cross-object change envelope = %+v, %v, %v", changes, more, err)
	}
}

func TestCrossObjectMutationFailureLeavesNoPartialObjects(t *testing.T) {
	t.Parallel()

	_, file, rows, routes, current, edge, leafID := mutationFixture(t)
	t.Cleanup(func() { _ = file.Close() })
	plan := revisedPlan(current, edge)
	// Fail on the last thing the transaction stages — the Route node — so the
	// Row, its history and the Relation are all already staged when it breaks.
	stale, err := routes.Get(leafID)
	if err != nil {
		t.Fatal(err)
	}
	stale.Synopsis = "conflicting rewrite of a revision that already exists"
	plan.Routes = []router.Node{stale}
	if err := New(file, rows, routes).Commit(plan); err == nil {
		t.Fatal("Commit() unexpectedly succeeded")
	}
	latest, err := rows.Read(current.ID)
	if err != nil || latest.Revision != 1 {
		t.Fatalf("latest Row after rollback = %#v, %v", latest, err)
	}
	records, _, err := rows.History(current.DatabaseID, current.TableID, current.ID, 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("History after rollback = %#v, %v", records, err)
	}
	live, err := rows.GetRelation(edge.ID, false)
	if err != nil || live.Revision != 1 || live.State != relation.StateLive {
		t.Fatalf("Relation after rollback = %#v, %v", live, err)
	}
	locators := openLeaf(t, routes, rows, leafID)
	if len(locators) != 1 || locators[0].Revision != 1 {
		t.Fatalf("mount after rollback = %#v", locators)
	}
	changes, more, err := nativechange.New(file).ListAfter(0, 10)
	if err != nil || more || len(changes) != 0 {
		t.Fatalf("failed mutation changes = %+v, %v, %v", changes, more, err)
	}
}

func TestRoutePlanStagingFailureLeavesNoPartialRouteOrChange(t *testing.T) {
	t.Parallel()
	_, file, rows, routes, _, _, _ := mutationFixture(t)
	t.Cleanup(func() { _ = file.Close() })
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	created := router.Node{Version: router.Version, ID: "route_staged", DatabaseID: "db_work", TableID: "tbl_notes",
		ParentID: "route_root", Name: "staged", Aliases: []string{}, Path: "/staged", Kind: router.KindLeaf,
		Purpose: "Staged", Revision: 1}
	duplicate := created
	duplicate.ID, duplicate.Name, duplicate.Path = "route_leaf", "duplicate", "/duplicate"
	_, err := New(file, rows, routes).CommitRoutePlan(RoutePlanCommit{
		Routes: []router.Node{created, duplicate}, Created: map[string]bool{created.ID: true, duplicate.ID: true},
		Metadata:    change.Metadata{Actor: "agent:test", Source: "event:test", Reason: "fault injection"},
		CommittedAt: now,
	})
	if err == nil {
		t.Fatal("CommitRoutePlan() unexpectedly succeeded")
	}
	if _, err := routes.Get(created.ID); !errors.Is(err, nativestore.ErrNotFound) {
		t.Fatalf("partially staged Route error = %v", err)
	}
	changes, _, err := nativechange.New(file).ListAfter(0, 10)
	if err != nil || len(changes) != 0 {
		t.Fatalf("failed Route plan changes = %#v, %v", changes, err)
	}
}

func countEntries(envelope change.Envelope, kind change.ObjectKind) int {
	count := 0
	for _, entry := range envelope.Entries {
		if entry.ObjectKind == kind {
			count++
		}
	}
	return count
}

// mutationFixture builds a database holding one Row mounted in one Route leaf.
// The mount lives on both ends — the leaf names the Row, the Row names the leaf
// — which is what replaced the Membership object.
func mutationFixture(t *testing.T) (string, *nativestore.File, *nativerow.Repository, *nativerouter.Repository, row.Row, relation.Relation, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 18, 0, 0, 0, time.UTC)
	column := catalog.Column{ID: "col_title", Name: "title", Type: "TEXT", MaxCharacters: 20, Purpose: "Title", SchemaVersion: 1, CreatedAt: now, UpdatedAt: now}
	table := catalog.Table{ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Purpose: "Notes", RowSemantics: "One note", SchemaVersion: 1, CreatedAt: now, UpdatedAt: now, Columns: []catalog.Column{column}}
	database := catalog.Database{ID: "db_work", Name: "work", Purpose: "Work", Scope: "Projects", SchemaVersion: 1, CreatedAt: now, UpdatedAt: now, Tables: []catalog.Table{table}}
	if err := nativecatalog.New(file).Write(nil, []catalog.Database{database}); err != nil {
		t.Fatal(err)
	}
	routes := nativerouter.New(file)
	root, err := routes.CreateRoot("route_root", database.ID, table.ID, "Root")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := routes.CreateChild("route_leaf", root.ID, "decisions", router.KindLeaf, "Decision rows")
	if err != nil {
		t.Fatal(err)
	}
	rows := nativerow.New(file)
	current := row.Row{ID: "row_source", DatabaseID: database.ID, TableID: table.ID, SchemaVersion: 1, Revision: 1, State: row.StateLive, Values: map[string]any{column.ID: "initial"}, CreatedAt: now, UpdatedAt: now, RouteLeafIDs: []string{leaf.ID}}
	target := row.Row{ID: "row_target", DatabaseID: database.ID, TableID: table.ID, SchemaVersion: 1, Revision: 1, State: row.StateLive, Values: map[string]any{column.ID: "target"}, CreatedAt: now, UpdatedAt: now}
	for _, value := range []row.Row{current, target} {
		if err := rows.Write(value); err != nil {
			t.Fatal(err)
		}
		if err := rows.AppendHistory(value, history.OperationInsert, row.WriteMetadata{}, now); err != nil {
			t.Fatal(err)
		}
	}
	edge := relation.Relation{Version: relation.Version, ID: "rel_edge", Source: relation.Endpoint{DatabaseID: database.ID, TableID: table.ID, RowID: current.ID}, Type: "depends_on", Target: relation.Endpoint{DatabaseID: database.ID, TableID: table.ID, RowID: target.ID}, Revision: 1, State: relation.StateLive, CreatedAt: now, UpdatedAt: now}
	if err := rows.PutRelation(edge); err != nil {
		t.Fatal(err)
	}
	mountLeaf(t, file, routes, leaf.ID, current.ID)
	return path, file, rows, routes, current, edge, leaf.ID
}

// mountLeaf points a Route leaf at a Row. The Row side of the mount is set by
// whoever wrote the Row; this only moves the leaf.
func mountLeaf(t *testing.T, file *nativestore.File, routes *nativerouter.Repository, leafID, rowID string) {
	t.Helper()
	leaf, err := routes.Get(leafID)
	if err != nil {
		t.Fatal(err)
	}
	leaf.RowID = rowID
	leaf.Revision++
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := routes.StageNode(transaction, leaf); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

func revisedPlan(current row.Row, edge relation.Relation) Plan {
	updated := current
	updated.Revision = 2
	updated.UpdatedAt = current.UpdatedAt.Add(time.Minute)
	updated.Values = map[string]any{"col_title": "revised"}
	deletedEdge := edge
	deletedEdge.Revision = 2
	deletedEdge.State = relation.StateDeleted
	deletedEdge.UpdatedAt = updated.UpdatedAt
	return Plan{Row: updated, Operation: history.OperationUpdate, Metadata: row.WriteMetadata{Actor: "agent:test", Source: "feedback", Reason: "correct"}, RecordedAt: updated.UpdatedAt, Relations: []relation.Relation{deletedEdge}}
}

// openLeaf reports what a leaf locates now, the way a read does: the leaf names
// the Row it holds, and the Row itself carries the revision.
func openLeaf(t *testing.T, routes *nativerouter.Repository, rows *nativerow.Repository, leafID string) []router.Locator {
	t.Helper()
	leaf, err := routes.Get(leafID)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.RowID == "" {
		return nil
	}
	value, err := rows.Read(leaf.RowID)
	if errors.Is(err, nativestore.ErrNotFound) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return []router.Locator{{
		DatabaseID: value.DatabaseID, TableID: value.TableID,
		RowID: value.ID, Revision: value.Revision,
	}}
}

// mountRowInLeaf mounts a Row in a leaf on both ends: the leaf names the Row,
// the Row names the leaf. Leaving either end out is the divergence the two
// fields exist to avoid.
func mountRowInLeaf(
	t *testing.T,
	file *nativestore.File,
	rows *nativerow.Repository,
	routes *nativerouter.Repository,
	leafID, rowID string,
) {
	t.Helper()
	mountLeaf(t, file, routes, leafID, rowID)
	value, err := rows.Read(rowID)
	if err != nil {
		t.Fatal(err)
	}
	for _, existing := range value.RouteLeafIDs {
		if existing == leafID {
			return
		}
	}
	value.RouteLeafIDs = append(append([]string(nil), value.RouteLeafIDs...), leafID)
	sort.Strings(value.RouteLeafIDs)
	value.Revision++
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := rows.StageRevision(transaction, value); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}
