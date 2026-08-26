package pagestoremigration

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/row"
)

// TestGenerationUsesOneSharedBufferPool is E3.5's gate.
//
// A generation used to build one buffer pool per Tree, each with its own
// capacity, because buffer.New was called inside OpenRuntime and the loader
// closure had the Tree's SpaceID baked in. Resident memory was therefore
// capacity × number of Trees.
//
// That is affordable at four fixed Trees and fatal at one Tree per Table
// (E4/E5): 16 MiB per Table means 1.6 GB at a hundred Tables, which contradicts
// "resident memory has an upper bound" outright. The frame key already carries
// SpaceID, so one pool can hold every Tree's pages; what was missing was a
// loader and writer that route by that SpaceID.
//
// See docs/storage/per-table-tree-v1.md §5.5.
func TestGenerationUsesOneSharedBufferPool(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	if _, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "shared pool"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
	}); err != nil {
		t.Fatal(err)
	}

	generation := authority.generation
	if len(generation.trees) < 2 {
		t.Fatalf("generation Trees = %d, want at least 2", len(generation.trees))
	}
	shared := generation.trees[0].runtime.Pool()
	if shared == nil {
		t.Fatal("Tree Runtime holds no buffer pool")
	}
	for _, tree := range generation.trees {
		if tree.runtime.Pool() != shared {
			t.Fatalf("%s Tree has its own buffer pool; resident memory grows with Tree count",
				tree.manifest.Kind)
		}
	}

	// One pool means one budget. Counting frames through any Tree reports the
	// whole generation's residency, which is the number that has to stay
	// bounded as Trees multiply.
	if frames := shared.Stats().Frames; frames > openGenerationFrames {
		t.Fatalf("resident frames = %d, over the one-pool budget of %d", frames, openGenerationFrames)
	}
}
