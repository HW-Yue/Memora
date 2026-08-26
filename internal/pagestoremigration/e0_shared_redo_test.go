package pagestoremigration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/row"
)

// TestGenerationUsesOneSharedRedoLog is E0 stage 1's gate.
//
// A generation used to open one WAL Segment Set per Tree — catalog.wal,
// current.wal, versions.wal, fulltext.wal. That is not how a redo log works in
// InnoDB and it costs correctness, not just tidiness: a publication that spans
// three Trees became three independent commits, so a crash between them left
// the Trees disagreeing. One log per generation makes the span one commit.
//
// See docs/storage/shared-circular-redo-v1.md §1 and §2.1.
func TestGenerationUsesOneSharedRedoLog(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, rows, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	if _, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "shared log"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
	}); err != nil {
		t.Fatal(err)
	}

	generationDirectory := filepath.Join(directory, GenerationDirectory)
	entries, err := os.ReadDir(generationDirectory)
	if err != nil {
		t.Fatal(err)
	}
	var walDirectories []string
	for _, entry := range entries {
		if entry.IsDir() && filepath.Ext(entry.Name()) == ".wal" {
			walDirectories = append(walDirectories, entry.Name())
		}
	}
	if len(walDirectories) != 1 || walDirectories[0] != sharedWALDirectory {
		t.Fatalf("generation WAL directories = %v, want exactly [%s]", walDirectories, sharedWALDirectory)
	}

	// The directory listing alone would pass if the Trees each opened their own
	// Segment Set over the same path. Requiring one shared instance is what
	// actually buys the single commit.
	generation := authority.generation
	if len(generation.trees) < 2 {
		t.Fatalf("generation Trees = %d, want at least 2", len(generation.trees))
	}
	shared := generation.log
	if shared == nil {
		t.Fatal("generation holds no shared redo log")
	}
	for _, tree := range generation.trees {
		if tree.set != shared {
			t.Fatalf("%s Tree does not use the generation's shared redo log", tree.manifest.Kind)
		}
	}
}

// TestSharedRedoLogRecoversEveryTreeOnReopen is the hard gate on E0 stage 1.
//
// Merging four logs into one changes what recovery replays: the log now
// interleaves Records from four spaces and every reopen routes each of them by
// SpaceID. Getting that wrong loses data silently, so the check is not "it
// still opens" but two stronger things:
//
//  1. every Tree reads back exactly what it held before the close — the Trees
//     are closed without flushing, so this is recovery's work, not the Page
//     files';
//  2. recovery is a fixed point. Page bytes are allowed to change on the first
//     reopen (that is the dirty Pages arriving from the log), but a second
//     reopen replaying the same log must land on the same bytes. A replay that
//     is not idempotent corrupts a little more on every restart.
func TestSharedRedoLogRecoversEveryTreeOnReopen(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, rows, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)

	// Touch all four Trees: the Catalog Tree already carries the Database and
	// Table, and each Row write lands in current, versions and fulltext.
	kept, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "kept alpha"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	revised, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "revised beta"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Update(ctx, "work", "notes", revised.ID, map[string]any{"title": "revised gamma"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: revised.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	removed, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "removed delta"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Delete(ctx, "work", "notes", removed.ID, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: removed.Revision,
	}); err != nil {
		t.Fatal(err)
	}

	fulltextBefore, err := fulltextSnapshotSHA256(authority.generation.fulltext)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}

	generationDirectory := filepath.Join(directory, GenerationDirectory)
	var recoveredPages map[string][]byte
	for attempt := 1; attempt <= 2; attempt++ {
		reopened, err := OpenAuthority(ctx, file, directory)
		if err != nil {
			t.Fatalf("reopen %d: %v", attempt, err)
		}
		fulltextAfter, err := fulltextSnapshotSHA256(reopened.generation.fulltext)
		if err != nil {
			t.Fatal(err)
		}
		if fulltextAfter != fulltextBefore {
			t.Fatalf("reopen %d fulltext snapshot = %s, want %s", attempt, fulltextAfter, fulltextBefore)
		}
		assertRowPosting(t, reopened, "kept", kept.ID, 1)
		assertRowPosting(t, reopened, "gamma", revised.ID, 2)
		assertNoRowPosting(t, reopened, "delta")
		assertNoRowPosting(t, reopened, "beta")
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}

		// The four fixed Trees plus one per Table. The count follows the
		// Catalog now, so it is read from the manifest rather than hardcoded.
		manifest, err := readManifest(generationDirectory)
		if err != nil {
			t.Fatal(err)
		}
		pages := generationPageBytes(t, generationDirectory)
		if len(pages) != len(manifest.Trees) || len(pages) < len(expectedTrees) {
			t.Fatalf("reopen %d Page files = %d, want %d", attempt, len(pages), len(manifest.Trees))
		}
		if attempt == 1 {
			recoveredPages = pages
			continue
		}
		for name, first := range recoveredPages {
			if !bytes.Equal(first, pages[name]) {
				t.Fatalf("replaying the log twice gave different %s bytes", name)
			}
		}
	}
}

func generationPageBytes(t *testing.T, generationDirectory string) map[string][]byte {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(generationDirectory, "*.pages"))
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][]byte, len(matches))
	for _, path := range matches {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		result[filepath.Base(path)] = content
	}
	return result
}

// TestPerTreeLogGenerationIsUpgradedOnOpen guards the one hole a shared log
// leaves open.
//
// A v3 generation is complete and healthy — four Trees, every document present,
// nothing for reconcile to do. Its only defect is structural: one redo log per
// Tree, so a publication spanning Trees cannot be one transaction. Writing to
// it would silently give up the atomicity stage 2 just bought, so the Authority
// rebuilds it by COW instead, exactly as it does for a generation missing a
// Tree.
func TestPerTreeLogGenerationIsUpgradedOnOpen(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := NewNativeReader(file)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reader.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	buildPerTreeLogGeneration(t, directory, plan)

	upgraded, err := OpenAuthority(ctx, file, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if upgraded.marker.Epoch != 1 || upgraded.marker.Generation == GenerationDirectory {
		t.Fatalf("per-Tree-log generation was not upgraded: %+v", upgraded.marker)
	}
	if upgraded.generation.log == nil {
		t.Fatal("upgraded generation still has no shared redo log")
	}
	assertCatalogPosting(t, upgraded.Generation(), "notes", fulltext.KindTable, table.ID, table.SchemaVersion)

	// The old generation is left untouched, same as every other COW upgrade.
	old, err := openLiveGeneration(filepath.Join(directory, GenerationDirectory))
	if err != nil {
		t.Fatalf("per-Tree-log generation was not preserved: %v", err)
	}
	defer old.Close()
	if old.log != nil {
		t.Fatal("preserved generation should still be the per-Tree-log one")
	}
}

// buildPerTreeLogGeneration writes a complete v3 generation: nothing is missing
// from it, it just predates the shared redo log.
func buildPerTreeLogGeneration(t *testing.T, directory string, plan Plan) {
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
	manifest := generationManifest{
		Version: treeWALGenerationVersion, PlanVersion: PlanVersion,
		PlanDigest: plan.Digest, SourceFingerprint: plan.SourceFingerprint,
		Trees: make([]treeManifest, len(treeWALExpectedTrees)),
	}
	for index, specification := range treeWALExpectedTrees {
		state, err := buildTreeWithOwnLog(target, specification, capacity, plan)
		if err != nil {
			t.Fatal(err)
		}
		specification.State = treeStateFromRuntime(state)
		manifest.Trees[index] = specification
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
