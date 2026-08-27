package pagestoremigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestGenerationAcceptsAPerTableHistoryTree is E5 stage 4's first gate.
//
// A Row's history is a flat object kind today (nativestore.ObjectKindHistory),
// keyed by (Row, revision) in the same record space as everything else — so one
// Row's revisions are scattered among every other Row's, and reading a history
// is a sequence of point lookups rather than one range scan.
//
// History becomes a Table, and a Table is a Tree. This gate is the identity
// half: a generation must accept a second derived Tree per Table, and refuse
// one whose identity does not follow from the Table it names.
//
// Nothing writes to it yet — that is the next stage, exactly as E4 stage 1 made
// the Tree set open-ended before anything grew a Tree.
//
// See docs/storage/per-table-tree-v1.md §4 and §5.
func TestGenerationAcceptsAPerTableHistoryTree(t *testing.T) {
	t.Parallel()

	manifest := generationManifest{
		Version: generationVersion, PlanVersion: PlanVersion,
		Trees: make([]treeManifest, 0, len(expectedTrees)+2),
	}
	state := treeStateManifest{Generation: 1, Revision: 1, RootPageID: 2, NextPageID: 3, LSN: 1}
	for _, specification := range expectedTrees {
		specification.State = state
		manifest.Trees = append(manifest.Trees, specification)
	}
	for _, specification := range []treeManifest{
		tableTreeManifest("tbl_notes"), historyTreeManifest("tbl_notes"),
	} {
		specification.State = state
		manifest.Trees = append(manifest.Trees, specification)
	}
	manifest.PlanDigest = zeroDigest
	manifest.SourceFingerprint = zeroDigest
	manifest.ContentDigest = zeroDigest
	digest, err := manifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Digest = digest
	if err := manifest.validate(); err != nil {
		t.Fatalf("a generation carrying a Table's history Tree must validate: %v", err)
	}

	// A history Tree must not be able to borrow its Table Tree's identity, or
	// the two would collide in the one space every Tree is registered under.
	if historyTreeManifest("tbl_notes").SpaceID == tableTreeManifest("tbl_notes").SpaceID {
		t.Fatal("a Table's history Tree shares its Table Tree's space")
	}

	forged := manifest
	forged.Trees = append([]treeManifest(nil), manifest.Trees...)
	forged.Trees[len(forged.Trees)-1].PageFile = "forged.pages"
	forged.Digest, err = manifestDigest(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := forged.validate(); !errors.Is(err, ErrTargetCorrupt) {
		t.Fatalf("a history Tree with a forged page file must be refused, got %v", err)
	}
}

// TestCreatingATableCreatesItsHistoryTree is the other half: the Tree set does
// not merely tolerate a history Tree, it grows one with the Table, and the
// growth survives a reopen.
func TestCreatingATableCreatesItsHistoryTree(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)

	if !authority.generation.HistoryTree(table.ID) {
		t.Fatalf("Table %q has no history Tree", table.ID)
	}
	generationDirectory := filepath.Join(directory, GenerationDirectory)
	specification := historyTreeManifest(table.ID)
	if _, err := os.Stat(filepath.Join(generationDirectory, specification.PageFile)); err != nil {
		t.Fatalf("history Tree page file: %v", err)
	}

	manifest, err := readManifest(generationDirectory)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tree := range manifest.Trees {
		found = found || tree.Kind == historyTreeKind(table.ID)
	}
	if !found {
		t.Fatalf("manifest Trees = %#v, want a history Tree for %q", manifest.Trees, table.ID)
	}
	if err := manifest.validate(); err != nil {
		t.Fatalf("rewritten manifest must validate: %v", err)
	}

	reopened, err := openLiveGeneration(generationDirectory)
	if err != nil {
		t.Fatalf("reopen generation: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if !reopened.HistoryTree(table.ID) {
		t.Fatalf("reopened generation lost the history Tree for %q", table.ID)
	}
}
