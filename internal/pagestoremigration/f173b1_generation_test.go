package pagestoremigration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalogfulltext"
	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/router"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
	"github.com/HW-Yue/Memora/internal/store/wal"
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
	if manifest.Version != "memora.page-index-generation/v4" ||
		manifest.PlanVersion != "memora.page-index-migration-plan/v3" {
		t.Fatalf("generation v4 manifest = %#v", manifest)
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

func TestV2AuthorityIncrementallyReconcilesRouteAndDeletedTombstone(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	routes := nativerouter.New(file)
	root, leaf := createDirectRouteFixture(t, routes, table.DatabaseID, table.ID)
	plan := currentPlan(t, file)
	replaceWithCatalogRowV2Generation(t, directory, plan)

	reopened, err := OpenAuthority(ctx, file, directory)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.marker.Epoch != 0 {
		t.Fatalf("revision-one Route unexpectedly forced COW: %+v", reopened.marker)
	}
	assertCatalogPosting(t, reopened.Generation(), "architecture", fulltext.KindRoute, leaf.ID, 1)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	leaf.Revision, leaf.Deleted = 2, true
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
	reopened, err = OpenAuthority(ctx, file, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if postings, err := reopened.Generation().Fulltext().Postings("architecture"); err != nil || len(postings) != 0 {
		t.Fatalf("deleted Route postings = %#v, %v", postings, err)
	}
	objects, err := reopened.Generation().Fulltext().Objects()
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if object.Kind == fulltext.KindRoute && object.ObjectID == leaf.ID {
			if object.State != fulltext.StateDeleted || object.Revision != 2 {
				t.Fatalf("Route tombstone = %#v", object)
			}
			return
		}
	}
	t.Fatalf("Route tombstone is missing; root=%s", root.ID)
}

func TestV2AuthorityUsesCOWForMissingRouteRevisionGap(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	routes := nativerouter.New(file)
	_, leaf := createDirectRouteFixture(t, routes, table.DatabaseID, table.ID)
	leaf.Revision, leaf.Synopsis = 2, "Gap recovered synopsis"
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
	plan := currentPlan(t, file)
	replaceWithCatalogRowV2Generation(t, directory, plan)

	reopened, err := OpenAuthority(ctx, file, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.marker.Epoch != 1 || reopened.marker.Generation == GenerationDirectory {
		t.Fatalf("Route revision gap did not COW: %+v", reopened.marker)
	}
	assertCatalogPosting(t, reopened.Generation(), "recovered", fulltext.KindRoute, leaf.ID, 2)
	old, err := openLiveGeneration(filepath.Join(directory, GenerationDirectory))
	if err != nil {
		t.Fatalf("old v2 generation was not preserved: %v", err)
	}
	defer old.Close()
	if postings, err := old.Fulltext().Postings("architecture"); err != nil || len(postings) != 0 {
		t.Fatalf("old v2 generation changed = %#v, %v", postings, err)
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

func replaceWithCatalogRowV2Generation(t *testing.T, directory string, plan Plan) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(directory, GenerationDirectory)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, AuthorityMarkerFilename)); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, GenerationDirectory)
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	capacity, err := migrationCapacity(plan)
	if err != nil {
		t.Fatal(err)
	}
	catalogDocuments, err := catalogfulltext.Project(plan.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	rowValues, err := rowDocuments(plan)
	if err != nil {
		t.Fatal(err)
	}
	documents := append(catalogDocuments, rowValues...)
	// Current format, one shared redo log. What makes this generation look
	// pre-F173b is the missing Route documents, not the log layout — a per-Tree
	// log would be upgraded by COW on open and hide the incremental reconcile
	// this fixture exists to exercise.
	manifest := generationManifest{
		Version: generationVersion, PlanVersion: PlanVersion,
		PlanDigest: plan.Digest, SourceFingerprint: plan.SourceFingerprint,
		Trees: make([]treeManifest, len(expectedTrees)),
	}
	log, err := wal.CreateSegmentSet(filepath.Join(target, sharedWALDirectory), 0)
	if err != nil {
		t.Fatal(err)
	}
	for index, specification := range expectedTrees {
		var state treecontrol.State
		if specification.Kind == "fulltext" {
			state = buildRowOnlyFulltextTree(t, target, specification, capacity, documents, log)
		} else {
			state, err = buildTree(target, specification, capacity, plan, log)
			if err != nil {
				t.Fatal(err)
			}
		}
		specification.State = treeStateFromRuntime(state)
		manifest.Trees[index] = specification
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	manifest.ContentDigest, err = contentDigest(target, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(target, manifest); err != nil {
		t.Fatal(err)
	}
	marker, err := newAuthorityMarker(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAuthorityMarker(directory, marker); err != nil {
		t.Fatal(err)
	}
}
