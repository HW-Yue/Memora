package pagestoremigration

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/store/fulltextindex"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

func TestRowOnlyV2AuthorityAutomaticallyUpgradesCatalogByCOW(t *testing.T) {
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
	if err := os.RemoveAll(filepath.Join(directory, GenerationDirectory)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, AuthorityMarkerFilename)); err != nil {
		t.Fatal(err)
	}
	buildRowOnlyV2Generation(t, directory, plan)

	upgraded, err := OpenAuthority(ctx, file, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if upgraded.marker.Epoch != 1 || upgraded.marker.Generation == GenerationDirectory {
		t.Fatalf("row-only generation did not COW upgrade: %+v", upgraded.marker)
	}
	assertCatalogPosting(t, upgraded.Generation(), "work", fulltext.KindDatabase, plan.Catalog[0].ID, plan.Catalog[0].SchemaVersion)
	assertCatalogPosting(t, upgraded.Generation(), "notes", fulltext.KindTable, table.ID, table.SchemaVersion)

	old, err := openLiveGeneration(filepath.Join(directory, GenerationDirectory))
	if err != nil {
		t.Fatalf("old row-only generation was not preserved: %v", err)
	}
	defer old.Close()
	if postings, err := old.Fulltext().Postings("work"); err != nil || len(postings) != 0 {
		t.Fatalf("old row-only generation changed: %#v, %v", postings, err)
	}
}

func buildRowOnlyV2Generation(t *testing.T, directory string, plan Plan) {
	t.Helper()
	target := filepath.Join(directory, GenerationDirectory)
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	capacity, err := migrationCapacity(plan)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := rowDocuments(plan)
	if err != nil {
		t.Fatal(err)
	}
	manifest := generationManifest{
		Version: rowGenerationVersion, PlanVersion: rowPlanVersion,
		PlanDigest: plan.Digest, SourceFingerprint: plan.SourceFingerprint,
		Trees: make([]treeManifest, len(treeWALExpectedTrees)),
	}
	for index, specification := range treeWALExpectedTrees {
		var state treecontrol.State
		if specification.Kind == "fulltext" {
			state = buildRowOnlyFulltextTree(t, target, specification, capacity, documents)
		} else {
			state, err = buildTreeWithOwnLog(target, specification, capacity, plan)
			if err != nil {
				t.Fatal(err)
			}
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

func buildRowOnlyFulltextTree(
	t *testing.T,
	directory string,
	specification treeManifest,
	capacity uint64,
	documents []fulltext.Document,
) treecontrol.State {
	t.Helper()
	set, err := wal.CreateSegmentSet(filepath.Join(directory, specification.WALDirectory), 0)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := page.Create(filepath.Join(directory, specification.PageFile), specification.SpaceID)
	if err != nil {
		_ = set.Close()
		t.Fatal(err)
	}
	runtime, _, err := treecommit.OpenRuntime(set, manager, treecommit.RuntimeConfig{
		SpaceID: specification.SpaceID, Capacity: capacity, OldFrames: max(uint64(1), capacity/2),
	})
	if err != nil {
		_ = set.Close()
		_ = manager.Close()
		t.Fatal(err)
	}
	index, err := fulltextindex.Open(runtime)
	if err == nil {
		_, err = index.Bootstrap(1, documents)
	}
	if err != nil {
		_ = set.Close()
		_ = manager.Close()
		t.Fatal(err)
	}
	report, err := runtime.FlushDirty(math.MaxUint64)
	if err != nil || report.Remaining != 0 {
		_ = set.Close()
		_ = manager.Close()
		t.Fatalf("flush row-only Fulltext tree: remaining=%d error=%v", report.Remaining, err)
	}
	if err := manager.Sync(); err != nil {
		_ = set.Close()
		_ = manager.Close()
		t.Fatal(err)
	}
	state := runtime.State()
	if err := set.Close(); err != nil {
		_ = manager.Close()
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	return state
}
