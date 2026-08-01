package nativemutation

import (
	"errors"
	"path/filepath"
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

	path, file, rows, routes, current, edge, membership := mutationFixture(t)
	plan := revisedPlan(current, edge, membership)
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
	locators, truncated, err := routes.Open(membership.LeafID, 10)
	if err != nil || truncated || len(locators) != 1 || locators[0].Revision != 2 {
		t.Fatalf("Open() = %#v, %v, %v", locators, truncated, err)
	}
	memberships, err := routes.Memberships(current.ID)
	if err != nil || len(memberships) != 1 || memberships[0].MembershipRevision != 2 {
		t.Fatalf("Memberships() = %#v, %v", memberships, err)
	}
	changes, more, err := nativechange.New(reopened).ListAfter(0, 10)
	if err != nil || more || len(changes) != 1 || len(changes[0].Entries) != 3 ||
		countEntries(changes[0], change.ObjectRow) != 1 ||
		countEntries(changes[0], change.ObjectRelation) != 1 ||
		countEntries(changes[0], change.ObjectRouteMembership) != 1 {
		t.Fatalf("cross-object change envelope = %+v, %v, %v", changes, more, err)
	}
}

func TestCrossObjectMutationFailureLeavesNoPartialObjects(t *testing.T) {
	t.Parallel()

	_, file, rows, routes, current, edge, membership := mutationFixture(t)
	t.Cleanup(func() { _ = file.Close() })
	plan := revisedPlan(current, edge, membership)
	plan.Memberships[0].MembershipRevision = 1 // duplicates the current physical membership.
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
	locators, _, err := routes.Open(membership.LeafID, 10)
	if err != nil || len(locators) != 1 || locators[0].Revision != 1 {
		t.Fatalf("membership after rollback = %#v, %v", locators, err)
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

func mutationFixture(t *testing.T) (string, *nativestore.File, *nativerow.Repository, *nativerouter.Repository, row.Row, relation.Relation, router.Membership) {
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
	if err := nativecatalog.New(file).Write([]catalog.Database{database}); err != nil {
		t.Fatal(err)
	}
	rows := nativerow.New(file)
	current := row.Row{ID: "row_source", DatabaseID: database.ID, TableID: table.ID, SchemaVersion: 1, Revision: 1, State: row.StateLive, Values: map[string]any{column.ID: "initial"}, CreatedAt: now, UpdatedAt: now}
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
	routes := nativerouter.New(file)
	root, err := routes.CreateRoot("route_root", database.ID, table.ID, "Root")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := routes.CreateChild("route_leaf", root.ID, "decisions", router.KindLeaf, "Decision rows")
	if err != nil {
		t.Fatal(err)
	}
	membership := router.Membership{LeafID: leaf.ID, MembershipRevision: 1, Locator: router.Locator{DatabaseID: database.ID, TableID: table.ID, RowID: current.ID, Revision: 1}}
	if err := routes.Attach(leaf.ID, membership.Locator, membership.MembershipRevision); err != nil {
		t.Fatal(err)
	}
	return path, file, rows, routes, current, edge, membership
}

func revisedPlan(current row.Row, edge relation.Relation, membership router.Membership) Plan {
	updated := current
	updated.Revision = 2
	updated.UpdatedAt = current.UpdatedAt.Add(time.Minute)
	updated.Values = map[string]any{"col_title": "revised"}
	deletedEdge := edge
	deletedEdge.Revision = 2
	deletedEdge.State = relation.StateDeleted
	deletedEdge.UpdatedAt = updated.UpdatedAt
	membership.MembershipRevision = 2
	membership.Revision = 2
	return Plan{Row: updated, Operation: history.OperationUpdate, Metadata: row.WriteMetadata{Actor: "agent:test", Source: "feedback", Reason: "correct"}, RecordedAt: updated.UpdatedAt, Relations: []relation.Relation{deletedEdge}, Memberships: []router.Membership{membership}}
}
