package pagestoremigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store/currentrowindex"
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

// TestCreatingATableCreatesItsTree is the other half of E4 stage 1: the Tree
// set does not just tolerate a per-Table Tree, it grows one when a Table is
// created, and the growth survives a reopen.
func TestCreatingATableCreatesItsTree(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)

	if !authority.generation.TableTree(table.ID) {
		t.Fatalf("Table %q has no Tree", table.ID)
	}
	specification := tableTreeManifest(table.ID)
	generationDirectory := filepath.Join(directory, GenerationDirectory)
	if _, err := os.Stat(filepath.Join(generationDirectory, specification.PageFile)); err != nil {
		t.Fatalf("Table Tree page file: %v", err)
	}

	// The manifest is what makes the Tree part of the generation rather than a
	// stray file the next open would reject.
	manifest, err := readManifest(generationDirectory)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tree := range manifest.Trees {
		if tree.Kind == tableTreeKind(table.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("manifest Trees = %#v, want one for %q", manifest.Trees, table.ID)
	}
	if err := manifest.validate(); err != nil {
		t.Fatalf("rewritten manifest must validate: %v", err)
	}

	reopened, err := openLiveGeneration(generationDirectory)
	if err != nil {
		t.Fatalf("reopen generation: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if !reopened.TableTree(table.ID) {
		t.Fatalf("reopened generation lost the Tree for %q", table.ID)
	}
}

// TestScanningOneTableNeverReachesAnother is E4 stage 2's gate.
//
// Every Table's current Rows used to sit in one Tree keyed by (Table, Row), so
// "scan a Table" meant "walk the whole index and match a prefix" — the Table
// was a filter, not a partition. A Table now has a Tree, and the key inside it
// is the Row ID alone.
//
// See docs/storage/per-table-tree-v1.md §2.
func TestScanningOneTableNeverReachesAnother(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	dictionary, rows, notes, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	tasks, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "tasks", Purpose: "Tasks", RowSemantics: "One task",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT", Purpose: "Title"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	noteRow, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "a note"}, row.WriteOptions{
		ExpectedSchemaVersion: notes.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	taskRow, err := rows.Insert(ctx, "work", "tasks", map[string]any{"title": "a task"}, row.WriteOptions{
		ExpectedSchemaVersion: tasks.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}

	generation := authority.generation
	for _, want := range []struct {
		tableID string
		rowID   string
		other   string
	}{
		{tableID: notes.ID, rowID: noteRow.ID, other: taskRow.ID},
		{tableID: tasks.ID, rowID: taskRow.ID, other: noteRow.ID},
	} {
		index := generation.CurrentRowsFor(want.tableID)
		if index == nil {
			t.Fatalf("Table %q has no Tree", want.tableID)
		}
		page, err := index.Page("", 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Locators) != 1 || page.Locators[0].RowID != want.rowID ||
			page.Locators[0].TableID != want.tableID {
			t.Fatalf("Table %q scan = %#v, want only %q", want.tableID, page.Locators, want.rowID)
		}
		// The decisive half: the other Table's Row is not merely filtered out
		// of the scan, it is not in this Tree at all.
		if _, err := index.Lookup(want.other); !errors.Is(err, currentrowindex.ErrNotFound) {
			t.Fatalf("Table %q found %q, error = %v", want.tableID, want.other, err)
		}
	}

	// And the reader routing on top of them agrees.
	reader := tableCurrentRows{generation: generation}
	if _, err := reader.Lookup(notes.ID, taskRow.ID); !errors.Is(err, currentrowindex.ErrNotFound) {
		t.Fatalf("cross-Table lookup through the reader = %v", err)
	}
	if locator, err := reader.Lookup(tasks.ID, taskRow.ID); err != nil || locator.RowID != taskRow.ID {
		t.Fatalf("same-Table lookup through the reader = %#v, %v", locator, err)
	}
}
