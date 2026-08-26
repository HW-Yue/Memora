package pagestoremigration

import (
	"errors"
	"testing"
)

// TestGenerationAcceptsAPerTableTree is E4 stage 1's gate.
//
// A generation's Tree set was a fixed list of four: the manifest was validated
// positionally against expectedTrees, so a fifth Tree made the whole generation
// read as corrupt. "One Tree per Table" cannot be built on a Tree set that
// cannot grow.
//
// This stage only makes the set open-ended. Nothing creates a per-Table Tree
// yet; that is stage 2.
//
// See docs/storage/per-table-tree-v1.md §5.
func TestGenerationAcceptsAPerTableTree(t *testing.T) {
	t.Parallel()

	manifest := generationManifest{
		Version: generationVersion, PlanVersion: PlanVersion,
		Trees: make([]treeManifest, 0, len(expectedTrees)+1),
	}
	for _, specification := range expectedTrees {
		specification.State = treeStateManifest{Generation: 1, Revision: 1, RootPageID: 2, NextPageID: 3, LSN: 1}
		manifest.Trees = append(manifest.Trees, specification)
	}
	manifest.Trees = append(manifest.Trees, tableTreeManifest("tbl_notes"))
	manifest.Trees[len(manifest.Trees)-1].State = treeStateManifest{
		Generation: 1, Revision: 1, RootPageID: 2, NextPageID: 3, LSN: 1,
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
		t.Fatalf("a generation carrying a per-Table Tree must validate: %v", err)
	}

	// The set is open-ended, not unchecked: a Tree whose identity does not
	// derive from its Table is still corrupt.
	forged := manifest
	forged.Trees = append([]treeManifest(nil), manifest.Trees...)
	forged.Trees[len(forged.Trees)-1].SpaceID++
	forged.Digest, err = manifestDigest(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := forged.validate(); !errors.Is(err, ErrTargetCorrupt) {
		t.Fatalf("a per-Table Tree with a forged space must be refused, got %v", err)
	}
}

const zeroDigest = "0000000000000000000000000000000000000000000000000000000000000000"
