package pagestoremigration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/nativemutation"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/router"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestAuthorityPublishesDirectRouteCRUDAndRenameSubtree(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, _, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	root, err := rows.CreateTableRouterRoot(ctx, "work", "notes", "All notes", "")
	if err != nil {
		t.Fatal(err)
	}
	branch, err := rows.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "architecture", Kind: router.KindBranch, Purpose: "Architecture decisions",
	})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := rows.CreateRouterNode(ctx, branch.ID, router.NodeDefinition{
		Name: "storage", Kind: router.KindLeaf, Purpose: "Storage decisions",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRoutePosting(t, authority, "architecture", branch.ID, "name", 1)
	assertRoutePosting(t, authority, "architecture", leaf.ID, "path", 1)

	renamed, err := rows.RenameRouterNode(ctx, branch.ID, "systems", branch.Revision)
	if err != nil {
		t.Fatal(err)
	}
	assertRoutePosting(t, authority, "systems", renamed.ID, "name", renamed.Revision)
	assertRoutePosting(t, authority, "architecture", renamed.ID, "aliases", renamed.Revision)
	leaf, err = rows.GetRouterNode(ctx, leaf.ID)
	if err != nil || leaf.Revision != 2 {
		t.Fatalf("renamed descendant = %#v, %v", leaf, err)
	}
	assertNoRoutePostingForObject(t, authority, "architecture", leaf.ID)
	assertRoutePosting(t, authority, "systems", leaf.ID, "path", leaf.Revision)

	leaf, err = rows.UpdateRouterSynopsis(ctx, leaf.ID, "Private durability boundaries", leaf.Revision)
	if err != nil {
		t.Fatal(err)
	}
	assertRoutePosting(t, authority, "durability", leaf.ID, "synopsis", leaf.Revision)
	deletedRevision, err := rows.DeleteRouterNode(ctx, leaf.ID, leaf.Revision)
	if err != nil {
		t.Fatal(err)
	}
	assertNoRoutePostingForObject(t, authority, "storage", leaf.ID)
	assertFulltextObject(t, authority, fulltext.KindRoute, leaf.ID, deletedRevision, fulltext.StateDeleted)
}

func TestCoordinatorRoutePlanPublishesMovedPathInOneFulltextRevision(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, _, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	root, err := rows.CreateTableRouterRoot(ctx, "work", "notes", "All notes", "")
	if err != nil {
		t.Fatal(err)
	}
	left, err := rows.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "sourcebranch", Kind: router.KindBranch, Purpose: "Source",
	})
	if err != nil {
		t.Fatal(err)
	}
	right, err := rows.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "targetbranch", Kind: router.KindBranch, Purpose: "Target",
	})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := rows.CreateRouterNode(ctx, left.ID, router.NodeDefinition{
		Name: "moveme", Kind: router.KindLeaf, Purpose: "Moved leaf",
	})
	if err != nil {
		t.Fatal(err)
	}
	release, err := rows.BeginAuthorityWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	moved := leaf
	moved.ParentID, moved.Path, moved.Revision = right.ID, right.Path+"/"+leaf.Name, leaf.Revision+1
	before := fulltextTreeRevision(t, authority)
	_, err = nativemutation.New(file, nativerow.New(file), nativerouter.New(file), authority).CommitRoutePlan(
		nativemutation.RoutePlanCommit{
			Routes: []router.Node{moved}, Created: map[string]bool{},
			Metadata:    change.Metadata{Actor: "agent:test", Source: "event:move", Reason: "move Route"},
			CommittedAt: time.Now().UTC(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if after := fulltextTreeRevision(t, authority); after != before+1 {
		t.Fatalf("Route Plan Fulltext revision = %d, want %d", after, before+1)
	}
	assertNoRoutePostingForObject(t, authority, "sourcebranch", leaf.ID)
	assertRoutePosting(t, authority, "targetbranch", leaf.ID, "path", moved.Revision)
}

func TestCoordinatorRoutePlanPublishesCreateAndDeleteInOneFulltextRevision(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, _, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	root, err := rows.CreateTableRouterRoot(ctx, "work", "notes", "All notes", "")
	if err != nil {
		t.Fatal(err)
	}
	removed, err := rows.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "retired", Kind: router.KindLeaf, Purpose: "Retired leaf",
	})
	if err != nil {
		t.Fatal(err)
	}
	created := router.Node{
		Version: router.Version, ID: "route_replacement", DatabaseID: root.DatabaseID, TableID: root.TableID,
		ParentID: root.ID, Name: "replacement", Path: "/replacement", Kind: router.KindLeaf,
		Purpose: "Replacement leaf", Revision: 1,
	}
	removed.Revision, removed.Deleted = removed.Revision+1, true
	release, err := rows.BeginAuthorityWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	before := fulltextTreeRevision(t, authority)
	_, err = nativemutation.New(file, nativerow.New(file), nativerouter.New(file), authority).CommitRoutePlan(
		nativemutation.RoutePlanCommit{
			Routes: []router.Node{created, removed}, Created: map[string]bool{created.ID: true},
			Metadata:    change.Metadata{Actor: "agent:test", Source: "event:replace", Reason: "replace Route"},
			CommittedAt: time.Now().UTC(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if after := fulltextTreeRevision(t, authority); after != before+1 {
		t.Fatalf("Route Plan Fulltext revision = %d, want %d", after, before+1)
	}
	assertRoutePosting(t, authority, "replacement", created.ID, "name", created.Revision)
	assertNoRoutePostingForObject(t, authority, "retired", removed.ID)
	assertFulltextObject(t, authority, fulltext.KindRoute, removed.ID, removed.Revision, fulltext.StateDeleted)
}

func TestCoordinatorPublishesRowAndRouteInOneFulltextRevision(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, table, inserted := authorityValues(t, ctx, file, authority)
	root, err := rows.CreateTableRouterRoot(ctx, "work", "notes", "All notes", "")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := rows.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "combined", Kind: router.KindLeaf, Purpose: "Combined mutation",
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := nativerow.New(file).Read(inserted.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	body.Revision, body.CommitSequence, body.UpdatedAt = 2, 0, now
	body.Values[table.Columns[0].ID] = "rowcombined"
	leaf.Revision, leaf.Synopsis = 2, "routecombined"
	before := fulltextTreeRevision(t, authority)
	err = nativemutation.New(file, nativerow.New(file), nativerouter.New(file), authority).Commit(nativemutation.Plan{
		Changes: []nativemutation.RowChange{{
			Row: body, Operation: history.OperationUpdate, RecordedAt: now,
		}},
		Routes: []router.Node{leaf},
	})
	if err != nil {
		t.Fatal(err)
	}
	if after := fulltextTreeRevision(t, authority); after != before+1 {
		t.Fatalf("combined Fulltext revision = %d, want %d", after, before+1)
	}
	assertRowPosting(t, authority, "rowcombined", body.ID, body.Revision)
	assertRoutePosting(t, authority, "routecombined", leaf.ID, "synopsis", leaf.Revision)
}

func TestAuthorityRejectsInvalidRouteProjectionBeforeBodyCommit(t *testing.T) {
	ctx := context.Background()
	_, _, authority := newAuthorityFixture(t)
	committed := false
	err := authority.PublishMutation(ctx, nil, []router.Node{{
		Version: router.Version, ID: "route_invalid", DatabaseID: "db_missing", TableID: "tbl_missing",
		Name: "invalid", Path: "/", Kind: router.KindRoot, Purpose: "Invalid",
	}}, func() error {
		committed = true
		return nil
	})
	if err == nil || committed {
		t.Fatalf("invalid Route projection commit=%v error=%v", committed, err)
	}
	if _, err := authority.Capture(ctx); err != nil {
		t.Fatalf("preflight error poisoned Authority: %v", err)
	}
}

func TestAuthorityRoutePublicationFaultsPoisonAndReopenConverges(t *testing.T) {
	for _, phase := range []authorityPhase{phaseRouteBodyCommitted, phaseRouteFulltextPublished} {
		t.Run(string(phase), func(t *testing.T) {
			ctx := context.Background()
			directory, file, authority := newAuthorityFixture(t)
			_, rows, _, _ := authorityValuesWithoutRow(t, ctx, file, authority)
			authority.checkpoint = func(current authorityPhase) error {
				if current == phase {
					return errors.New("injected Route publication fault")
				}
				return nil
			}
			root, err := rows.CreateTableRouterRoot(ctx, "work", "notes", "Recovered route", "")
			if !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("Route %s fault error = %v", phase, err)
			}
			// F226: reads stay available; the affected Database fails closed for writes.
			if _, err := authority.Capture(ctx); err != nil {
				t.Fatalf("Capture() after fault = %v, want success", err)
			}
			assertDatabaseWritesPoisoned(t, ctx, authority, root.DatabaseID)
			authority.checkpoint = nil
			if err := authority.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			reopenedFile, err := nativestore.Open(filepath.Join(directory, "database.memora"))
			if err != nil {
				t.Fatal(err)
			}
			defer reopenedFile.Close()
			reopened, err := OpenAuthority(ctx, reopenedFile, directory)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			assertRoutePosting(t, reopened, "recovered", root.ID, "purpose", root.Revision)
		})
	}
}

func TestAuthorityRouteFulltextWALFaultPoisonsAndReopenConverges(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, rows, _, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	for _, tree := range authority.generation.trees {
		if tree.manifest.Kind == "fulltext" {
			if err := tree.set.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	root, err := rows.CreateTableRouterRoot(ctx, "work", "notes", "WAL recovered route", "")
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("Route Fulltext WAL fault error = %v", err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedFile, err := nativestore.Open(filepath.Join(directory, "database.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedFile.Close()
	reopened, err := OpenAuthority(ctx, reopenedFile, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertRoutePosting(t, reopened, "recovered", root.ID, "purpose", root.Revision)
}

func TestAuthorityReopenPublishedRouteDoesNotAppendFulltextWAL(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, rows, _, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	root, err := rows.CreateTableRouterRoot(ctx, "work", "notes", "Stable route", "")
	if err != nil {
		t.Fatal(err)
	}
	nextBefore, err := authority.nextTransactionID("fulltext")
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedFile, err := nativestore.Open(filepath.Join(directory, "database.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedFile.Close()
	reopened, err := OpenAuthority(ctx, reopenedFile, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	nextAfter, err := reopened.nextTransactionID("fulltext")
	if err != nil {
		t.Fatal(err)
	}
	if nextAfter != nextBefore {
		t.Fatalf("Route replay appended Fulltext WAL transaction: before=%d after=%d", nextBefore, nextAfter)
	}
	assertRoutePosting(t, reopened, "stable", root.ID, "purpose", root.Revision)
}

func assertRoutePosting(
	t *testing.T, authority *Authority, term, routeID, fieldID string, revision uint64,
) {
	t.Helper()
	postings, err := authority.Generation().Fulltext().Postings(term)
	if err != nil {
		t.Fatal(err)
	}
	for _, posting := range postings {
		if posting.Kind == fulltext.KindRoute && posting.ObjectID == routeID && posting.FieldID == fieldID {
			if posting.Revision != revision {
				t.Fatalf("Route Postings(%q) stale = %#v", term, posting)
			}
			return
		}
	}
	t.Fatalf("Route Postings(%q) = %#v", term, postings)
}

func assertNoRoutePostingForObject(t *testing.T, authority *Authority, term, routeID string) {
	t.Helper()
	postings, err := authority.Generation().Fulltext().Postings(term)
	if err != nil {
		t.Fatal(err)
	}
	for _, posting := range postings {
		if posting.Kind == fulltext.KindRoute && posting.ObjectID == routeID {
			t.Fatalf("stale Route Postings(%q) = %#v", term, postings)
		}
	}
}

func assertFulltextObject(
	t *testing.T, authority *Authority, kind fulltext.ObjectKind, objectID string, revision uint64, state fulltext.State,
) {
	t.Helper()
	objects, err := authority.Generation().Fulltext().Objects()
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if object.Kind == kind && object.ObjectID == objectID {
			if object.Revision != revision || object.State != state {
				t.Fatalf("Fulltext object = %#v", object)
			}
			return
		}
	}
	t.Fatalf("Fulltext object %s/%s is missing", kind, objectID)
}
