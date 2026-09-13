package pagestoremigration

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/router"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestGenerationV3SeedIncludesCatalogRouteAndRowDocuments(t *testing.T) {
	directory := t.TempDir()
	reader, plan, _ := faultPlan(t)
	applier, err := NewApplier(reader, directory)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := applier.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := readManifest(receipt.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "memora.page-index-generation/v8" ||
		manifest.PlanVersion != "memora.page-index-migration-plan/v3" {
		t.Fatalf("generation v8 manifest = %#v", manifest)
	}
	generation, err := OpenGeneration(receipt.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer generation.Close()
	assertCatalogPosting(t, generation, "work", fulltext.KindDatabase, "db_work", 1)
	assertCatalogPosting(t, generation, "architecture", fulltext.KindRoute, "route_branch", 2)
	objects, err := generation.Fulltext().Objects()
	if err != nil {
		t.Fatal(err)
	}
	foundDeletedRow := false
	for _, object := range objects {
		foundDeletedRow = foundDeletedRow ||
			(object.Kind == fulltext.KindRow && object.ObjectID == "row_one" && object.State == fulltext.StateDeleted)
	}
	if !foundDeletedRow {
		t.Fatalf("deleted current Row seed object = %#v", objects)
	}
}

// TestRoutesWrittenAroundTheAuthorityAreNotAbsorbed replaces two tests:
// TestV2AuthorityIncrementallyReconcilesRouteAndDeletedTombstone and
// TestAuthorityAbsorbsARouteFirstSeenAtALaterRevision.
//
// Both wrote Routes straight to the record log and asserted that opening the
// Authority pulled them into the generation — one incrementally, one several
// revisions in. That was the reconcile pass, and it was right while the record
// log was the authority.
//
// E8 stage 1 removed it, because the same pass that pulls a Route in is the one
// that pushes a Tree back to an archive which is now allowed to be behind. A
// Route that never reached a Tree is not a Route the Database has, and opening
// it again does not change that.
//
// What is still worth pinning is what the two old tests were really guarding:
// the open does not rebuild the generation over it either. Ignoring a stray
// record is cheap; rebuilding a generation to absorb one is the heavy machinery
// this stage exists to delete.
func TestRoutesWrittenAroundTheAuthorityAreNotAbsorbed(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	routes := nativerouter.New(file)
	_, leaf := createDirectRouteFixture(t, routes, table.DatabaseID, table.ID)

	reopened, err := OpenAuthority(ctx, file, directory)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.marker.Epoch != 0 || reopened.marker.Generation != GenerationDirectory {
		t.Fatalf("a record-log Route forced a generation rebuild: %+v", reopened.marker)
	}
	if postings, err := reopened.Generation().Fulltext().Postings("architecture"); err != nil ||
		len(postings) != 0 {
		t.Fatalf("record-log Route postings = %#v, %v, want none", postings, err)
	}
	if _, err := reopened.RouteObjects().Get(nativerouter.ObjectKind, leaf.ID); err == nil {
		t.Fatal("a Route that never reached a Tree was absorbed into the objects Tree")
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func createDirectRouteFixture(
	t *testing.T, routes *nativerouter.Repository, databaseID, tableID string,
) (router.Node, router.Node) {
	t.Helper()
	root, err := routes.CreateRoot("route_reopen_root", databaseID, tableID, "All notes")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := routes.CreateChild(
		"route_reopen_leaf", root.ID, "architecture", router.KindLeaf, "Architecture decisions",
	)
	if err != nil {
		t.Fatal(err)
	}
	return root, leaf
}

func currentPlan(t *testing.T, file *nativestore.File) Plan {
	t.Helper()
	reader, err := NewNativeReader(file)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reader.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
