package pagestoremigration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

// TestGenerationCarriesAnObjectsTree is E7 stage 1's gate.
//
// Every object that keeps one record per identity — Route, Relation,
// Configuration, SnapshotMeta, Opaque — is found today through
// nativestore.File.records: a map built by scanning the whole file at open,
// with no capacity and no eviction, and it is the *only* index those objects
// have. That violates architecture principle four, and it is the one violation
// that gets worse on its own: the map grows with how many records were ever
// written, so a Row edited a hundred times is a hundred entries that are never
// released.
//
// internal/store/objectindex was built to replace exactly that — its package
// comment names "no process-resident directory of every record that ever
// existed" as the goal — and has had zero production callers since it was
// written. This stage gives it one: the generation opens an objects Tree
// alongside the others.
//
// Nothing is migrated into it yet; that is stage 2 (Route) onward. What this
// pins is that the Tree exists, is built by the applier, and comes back after a
// reopen — the same shape E4 stage 1 used before anything grew a per-Table Tree.
//
// See docs/storage/physical-index-v1.md §4.
func TestGenerationCarriesAnObjectsTree(t *testing.T) {
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
	if manifest.Version != generationVersion {
		t.Fatalf("generation version = %q, want %q", manifest.Version, generationVersion)
	}
	found := false
	for _, tree := range manifest.Trees {
		found = found || tree.Kind == "objects"
	}
	if !found {
		t.Fatalf("manifest carries no objects Tree: %#v", manifest.Trees)
	}
	if _, err := os.Stat(filepath.Join(receipt.Directory, "objects.pages")); err != nil {
		t.Fatalf("objects Page file: %v", err)
	}

	generation, err := OpenGeneration(receipt.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer generation.Close()
	if generation.Objects() == nil {
		t.Fatal("a reopened generation has no objects Index")
	}
	if _, err := generation.Objects().Get(routeObjectKind, "route_missing"); err == nil {
		t.Fatal("the objects Tree resolved a Route that does not exist")
	}
}

// TestTheObjectsTreeIsSeededWithEveryCurrentRoute is E7 stage 2's build gate.
//
// The generation is derived: it must be reconstructible from the record log
// alone, so whatever the write path publishes into the objects Tree, a build
// from a Plan has to produce the same Tree. Route is the first object to move,
// so this is where that equivalence starts being checked.
//
// The bytes stored are the record log's own Route encoding, verbatim. Storing a
// re-encoding would make the Tree a translation of the authority rather than a
// copy of it, and every future codec change a two-sided migration.
func TestTheObjectsTreeIsSeededWithEveryCurrentRoute(t *testing.T) {
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
	generation, err := OpenGeneration(receipt.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer generation.Close()

	if len(plan.CurrentRoutes) == 0 {
		t.Fatal("the fixture Plan carries no Routes")
	}
	for _, node := range plan.CurrentRoutes {
		stored, err := generation.Objects().Lookup(routeObjectKind, node.ID)
		if err != nil {
			t.Fatalf("Route %q is not in the objects Tree: %v", node.ID, err)
		}
		if stored.Revision != node.Revision {
			t.Fatalf("Route %q stored at revision %d, want %d",
				node.ID, stored.Revision, node.Revision)
		}
		want, err := nativerouter.EncodeNode(node)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(stored.Body, want) {
			t.Fatalf("Route %q body in the Tree differs from its record encoding", node.ID)
		}
	}
	// Walking the kind returns exactly the current Routes and nothing else: the
	// Tree is what answers "every Route" once the read path moves over, so an
	// extra or a missing entry is a wrong answer, not a slow one.
	walked, err := generation.Objects().Page(routeObjectKind, "", len(plan.CurrentRoutes)+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(walked.Records) != len(plan.CurrentRoutes) || walked.Truncated {
		t.Fatalf("walking Routes returned %d of %d (truncated=%v)",
			len(walked.Records), len(plan.CurrentRoutes), walked.Truncated)
	}
}

// TestAGenerationWithoutAnObjectsTreeIsRebuilt covers the upgrade every
// existing database takes: a v5 generation has no objects Tree at all.
//
// It must not be readable as-is — a missing Tree is not a Tree with nothing in
// it — so the Authority rebuilds the generation by COW, the same path a v4
// database takes on the way to v5.
func TestAGenerationWithoutAnObjectsTreeIsRebuilt(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	plan := currentPlan(t, file)
	buildPreObjectsGeneration(t, directory, plan)

	reopened, err := OpenAuthority(ctx, file, directory)
	if err != nil {
		t.Fatalf("a pre-objects generation must open by rebuilding: %v", err)
	}
	defer reopened.Close()
	if reopened.generation.Objects() == nil {
		t.Fatal("the rebuilt generation still has no objects Index")
	}
	// The rebuild is a real one, not a relabel: the Catalog it produced still
	// answers, so nothing was lost on the way through.
	if _, err := reopened.DescribeTable(ctx, "work", table.Name); err != nil {
		t.Fatalf("Catalog after the rebuild: %v", err)
	}
}

// buildPreObjectsGeneration writes a v5 generation: the layout that had a Tree
// per Table but no objects Tree.
//
// It is built rather than produced by deleting a Tree from a v6 one, for the
// same reason the pre-history fixture is: a generation that once had the Tree
// still carries that space's Records in the shared redo log, so removing the
// Tree afterwards produces a database no version of this code ever wrote.
func buildPreObjectsGeneration(t *testing.T, directory string, plan Plan) {
	t.Helper()
	target := filepath.Join(directory, GenerationDirectory)
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, AuthorityMarkerFilename)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	capacity, err := migrationCapacity(plan)
	if err != nil {
		t.Fatal(err)
	}
	log, err := wal.CreateSegmentSetWithCapacity(
		filepath.Join(target, sharedWALDirectory), 0, walRingBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest := generationManifest{
		Version: perTableTreeVersion, PlanVersion: PlanVersion,
		PlanDigest: plan.Digest, SourceFingerprint: plan.SourceFingerprint,
	}
	specifications := append([]treeManifest(nil), perTableExpectedTrees...)
	for _, tableID := range planTableIDs(plan) {
		specifications = append(specifications,
			tableTreeManifest(tableID), historyTreeManifest(tableID))
	}
	for _, specification := range specifications {
		state, buildErr := buildTree(target, specification, capacity, plan, log)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		specification.State = treeStateFromRuntime(state)
		manifest.Trees = append(manifest.Trees, specification)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if manifest.ContentDigest, err = contentDigest(target, manifest); err != nil {
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
