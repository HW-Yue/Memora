package nativemutation

import (
	"errors"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestSplitThenMergeUpdatesSemanticStructureAtomically(t *testing.T) {
	t.Parallel()

	_, file, rows, routes, source, _, membership := mutationFixture(t)
	t.Cleanup(func() { _ = file.Close() })
	now := source.UpdatedAt.Add(time.Hour)
	root, err := routes.Get("route_root")
	if err != nil {
		t.Fatal(err)
	}
	superseded := source
	superseded.Revision, superseded.State, superseded.UpdatedAt = 2, row.StateSuperseded, now
	first := newReshapeRow(source, "row_first", "first", now)
	second := newReshapeRow(source, "row_second", "second", now)
	root.Revision, root.Purpose = 2, "Root after split"
	oldMembership := membership
	oldMembership.MembershipRevision, oldMembership.Revision, oldMembership.Deleted = 2, 2, true
	metadata := row.WriteMetadata{Actor: "agent:test", Source: "reshape", Reason: "semantic boundary"}
	plan := ReshapePlan{
		Sources: Plan{
			Changes: []RowChange{{Row: superseded, Metadata: metadata, RecordedAt: now}},
			Routes:  []router.Node{root},
			Memberships: []router.Membership{
				oldMembership,
				reshapeMembership(membership, first.ID, 1, 1, false),
				reshapeMembership(membership, second.ID, 1, 1, false),
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
	locators, _, err := routes.Open(membership.LeafID, 10)
	if err != nil || len(locators) != 2 {
		t.Fatalf("split memberships = %#v, %v", locators, err)
	}

	mergedAt := now.Add(time.Hour)
	first.Revision, first.State, first.UpdatedAt = 2, row.StateSuperseded, mergedAt
	second.Revision, second.State, second.UpdatedAt = 2, row.StateSuperseded, mergedAt
	merged := newReshapeRow(source, "row_merged", "merged", mergedAt)
	root.Revision, root.Purpose = 3, "Root after merge"
	mergePlan := ReshapePlan{
		Sources: Plan{
			Changes: []RowChange{{Row: first, Metadata: metadata, RecordedAt: mergedAt}, {Row: second, Metadata: metadata, RecordedAt: mergedAt}},
			Routes:  []router.Node{root},
			Memberships: []router.Membership{
				reshapeMembership(membership, first.ID, 2, 2, true),
				reshapeMembership(membership, second.ID, 2, 2, true),
				reshapeMembership(membership, merged.ID, 1, 1, false),
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
	locators, _, err = routes.Open(membership.LeafID, 10)
	if err != nil || len(locators) != 1 || locators[0].RowID != merged.ID {
		t.Fatalf("merged memberships = %#v, %v", locators, err)
	}
}

func newReshapeRow(source row.Row, id, title string, now time.Time) row.Row {
	return row.Row{ID: id, DatabaseID: source.DatabaseID, TableID: source.TableID, SchemaVersion: source.SchemaVersion, Revision: 1, State: row.StateLive, Values: map[string]any{"col_title": title}, CreatedAt: now, UpdatedAt: now}
}

func reshapeMembership(base router.Membership, rowID string, membershipRevision, rowRevision uint64, deleted bool) router.Membership {
	return router.Membership{LeafID: base.LeafID, MembershipRevision: membershipRevision, Deleted: deleted, Locator: router.Locator{DatabaseID: base.DatabaseID, TableID: base.TableID, RowID: rowID, Revision: rowRevision}}
}
