package pagestoremigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/treecontrol"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

func TestGenerationBuildIncludesDurableFulltextTree(t *testing.T) {
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
	// One redo log for the whole generation, not one per Tree: the fulltext
	// Tree has its own Page file but commits into the shared log.
	for relative, wantDirectory := range map[string]bool{
		"fulltext.pages": false, sharedWALDirectory: true,
	} {
		info, err := os.Stat(filepath.Join(receipt.Directory, relative))
		if err != nil || info.IsDir() != wantDirectory {
			t.Fatalf("generation entry %q = %+v, %v", relative, info, err)
		}
	}
	manifest, err := readManifest(receipt.Directory)
	if err != nil {
		t.Fatal(err)
	}
	// Three fixed Trees plus two per Table: its current Rows and its history.
	if manifest.Version != "memora.page-index-generation/v5" ||
		manifest.PlanVersion != "memora.page-index-migration-plan/v3" ||
		len(manifest.Trees) != len(expectedTrees)+2 {
		t.Fatalf("generation v5 manifest = %#v", manifest)
	}
}

func TestLegacyThreeTreeAuthorityAutomaticallyUpgradesByCOW(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, _, _, inserted := authorityValues(t, ctx, file, authority)
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
	buildLegacyGeneration(t, directory, plan)

	legacy, err := OpenGeneration(filepath.Join(directory, GenerationDirectory))
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Fulltext() != nil {
		t.Fatal("legacy generation unexpectedly exposed Fulltext Tree")
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := OpenAuthority(ctx, file, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if upgraded.marker.Epoch != 1 || upgraded.marker.Generation == GenerationDirectory ||
		upgraded.Generation().Fulltext() == nil {
		t.Fatalf("upgraded authority marker=%+v fulltext=%p", upgraded.marker, upgraded.Generation().Fulltext())
	}
	postings, err := upgraded.Generation().Fulltext().Postings("indexed")
	if err != nil || len(postings) != 1 || postings[0].ObjectID != inserted.ID {
		t.Fatalf("upgraded Row postings = %#v, %v", postings, err)
	}
	if _, err := os.Stat(filepath.Join(directory, GenerationDirectory)); err != nil {
		t.Fatalf("legacy generation was not preserved: %v", err)
	}
}

func buildLegacyGeneration(t *testing.T, directory string, plan Plan) {
	t.Helper()
	target := filepath.Join(directory, GenerationDirectory)
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	capacity, err := migrationCapacity(plan)
	if err != nil {
		t.Fatal(err)
	}
	manifest := generationManifest{
		Version: legacyGenerationVersion, PlanVersion: legacyPlanVersion,
		PlanDigest: plan.Digest, SourceFingerprint: plan.SourceFingerprint,
		Trees: make([]treeManifest, len(legacyExpectedTrees)),
	}
	for index, specification := range legacyExpectedTrees {
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

// buildTreeWithOwnLog builds one Tree of a pre-v4 generation, back when every
// Tree owned its own redo log. The production build path now creates one log
// for the whole generation, so these legacy fixtures make the per-Tree log
// themselves rather than keeping a second layout alive in apply.go.
func buildTreeWithOwnLog(
	directory string,
	specification treeManifest,
	capacity uint64,
	plan Plan,
) (treecontrol.State, error) {
	set, err := wal.CreateSegmentSet(filepath.Join(directory, specification.WALDirectory), 0)
	if err != nil {
		return treecontrol.State{}, err
	}
	state, err := buildTree(directory, specification, capacity, plan, set)
	return state, errors.Join(err, set.Close())
}
