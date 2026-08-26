package nativemutation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestPublicSplitIntoSameLeafRollsBackRowsHistoryRelationsAndMounts(t *testing.T) {
	t.Parallel()

	_, file, rows, routes, source, edge, leafID := mutationFixture(t)
	t.Cleanup(func() { _ = file.Close() })
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{})
	base := nativerow.NewService(rows, dictionary, nativerow.ServiceOptions{})
	service := NewService(base, dictionary, rows, routes, New(file, rows, routes))
	_, err := service.Split(
		context.Background(), "work", "notes", []string{source.ID},
		[]map[string]any{{"title": "first"}, {"title": "second"}},
		row.ReshapeOptions{
			ExpectedSchemaVersion: 1, ExpectedRevision: 1,
			TargetRouteLeafIDs:     [][]string{{leafID}, {leafID}},
			RelationTargetOrdinals: map[string]int{edge.ID: 1},
			Metadata:               row.WriteMetadata{Actor: "agent:test", Source: "test", Reason: "rollback"},
		},
	)
	if err == nil {
		t.Fatal("Split() unexpectedly succeeded")
	}
	latest, readErr := rows.Read(source.ID)
	if readErr != nil || latest.Revision != 1 || latest.State != row.StateLive {
		t.Fatalf("source after rollback = %#v, %v", latest, readErr)
	}
	records, _, historyErr := rows.History(source.DatabaseID, source.TableID, source.ID, 10)
	if historyErr != nil || len(records) != 1 {
		t.Fatalf("history after rollback = %#v, %v", records, historyErr)
	}
	liveRelation, relationErr := rows.GetRelation(edge.ID, false)
	if relationErr != nil || liveRelation.Revision != 1 {
		t.Fatalf("relation after rollback = %#v, %v", liveRelation, relationErr)
	}
	locators := openLeaf(t, routes, rows, leafID)
	if len(locators) != 1 || locators[0].RowID != source.ID {
		t.Fatalf("Route after rollback = %#v", locators)
	}
}

func TestSplitThenMergeUpdatesSemanticStructureAtomically(t *testing.T) {
	t.Parallel()

	_, file, rows, routes, source, _, leafID := mutationFixture(t)
	t.Cleanup(func() { _ = file.Close() })
	now := source.UpdatedAt.Add(time.Hour)
	root, err := routes.Get("route_root")
	if err != nil {
		t.Fatal(err)
	}
	secondLeaf, err := routes.CreateChild("route_second", root.ID, "second", router.KindLeaf, "Second split Row")
	if err != nil {
		t.Fatal(err)
	}
	superseded := source
	superseded.Revision, superseded.State, superseded.UpdatedAt = 2, row.StateSuperseded, now
	first := newReshapeRow(source, "row_first", "first", now, leafID)
	second := newReshapeRow(source, "row_second", "second", now, secondLeaf.ID)
	root.Revision, root.Purpose = 2, "Root after split"
	metadata := row.WriteMetadata{Actor: "agent:test", Source: "reshape", Reason: "semantic boundary"}
	plan := ReshapePlan{
		Sources: Plan{
			Changes: []RowChange{{Row: superseded, Metadata: metadata, RecordedAt: now}},
			// The leaves move in the same commit as the Rows: one takes the
			// first target, the other takes the second.
			Routes: []router.Node{
				root,
				mountedLeaf(t, routes, leafID, first.ID),
				mountedLeaf(t, routes, secondLeaf.ID, second.ID),
			},
		},
		Targets: []RowChange{{Row: first, Metadata: metadata, RecordedAt: now, Initial: true}, {Row: second, Metadata: metadata, RecordedAt: now, Initial: true}},
	}
	if err := New(file, rows, routes).Split(plan); err != nil {
		t.Fatalf("Split() error = %v", err)
	}
	if _, err := rows.Read(source.ID); !errors.Is(err, nativestore.ErrNotFound) {
		t.Fatalf("superseded source remains visible: %v", err)
	}
	for _, value := range []row.Row{first, second} {
		if _, err := rows.Read(value.ID); err != nil {
			t.Fatalf("split target %q missing: %v", value.ID, err)
		}
		records, _, err := rows.History(value.DatabaseID, value.TableID, value.ID, 10)
		if err != nil || len(records) != 1 || records[0].Operation != history.OperationSplit {
			t.Fatalf("split history %q = %#v, %v", value.ID, records, err)
		}
	}
	if got, _ := routes.Get(root.ID); got.Revision != 2 || got.Purpose != root.Purpose {
		t.Fatalf("split route = %#v", got)
	}
	for mountedIn, rowID := range map[string]string{leafID: first.ID, secondLeaf.ID: second.ID} {
		locators := openLeaf(t, routes, rows, mountedIn)
		if len(locators) != 1 || locators[0].RowID != rowID {
			t.Fatalf("split mount %s = %#v", mountedIn, locators)
		}
	}

	mergedAt := now.Add(time.Hour)
	first.Revision, first.State, first.UpdatedAt = 2, row.StateSuperseded, mergedAt
	second.Revision, second.State, second.UpdatedAt = 2, row.StateSuperseded, mergedAt
	merged := newReshapeRow(source, "row_merged", "merged", mergedAt, leafID)
	root.Revision, root.Purpose = 3, "Root after merge"
	mergePlan := ReshapePlan{
		Sources: Plan{
			Changes: []RowChange{{Row: first, Metadata: metadata, RecordedAt: mergedAt}, {Row: second, Metadata: metadata, RecordedAt: mergedAt}},
			Routes: []router.Node{
				root,
				mountedLeaf(t, routes, leafID, merged.ID),
				mountedLeaf(t, routes, secondLeaf.ID, ""),
			},
		},
		Targets: []RowChange{{Row: merged, Metadata: metadata, RecordedAt: mergedAt, Initial: true}},
	}
	if err := New(file, rows, routes).Merge(mergePlan); err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if got, err := rows.Read(merged.ID); err != nil || got.Values["col_title"] != "merged" {
		t.Fatalf("merged target = %#v, %v", got, err)
	}
	if locators := openLeaf(t, routes, rows, leafID); len(locators) != 1 || locators[0].RowID != merged.ID {
		t.Fatalf("merged mount = %#v", locators)
	}
	if locators := openLeaf(t, routes, rows, secondLeaf.ID); len(locators) != 0 {
		t.Fatalf("released mount = %#v", locators)
	}
}

func newReshapeRow(source row.Row, id, title string, now time.Time, leafIDs ...string) row.Row {
	return row.Row{ID: id, DatabaseID: source.DatabaseID, TableID: source.TableID, SchemaVersion: source.SchemaVersion, Revision: 1, State: row.StateLive, Values: map[string]any{"col_title": title}, CreatedAt: now, UpdatedAt: now, RouteLeafIDs: leafIDs}
}

// mountedLeaf reads a leaf and returns the next revision of it holding rowID,
// or holding nothing when rowID is empty.
func mountedLeaf(t *testing.T, routes *nativerouter.Repository, leafID, rowID string) router.Node {
	t.Helper()
	leaf, err := routes.Get(leafID)
	if err != nil {
		t.Fatal(err)
	}
	leaf.RowID = rowID
	leaf.Revision++
	return leaf
}
