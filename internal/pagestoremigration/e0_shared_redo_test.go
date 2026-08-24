package pagestoremigration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

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

		pages := generationPageBytes(t, generationDirectory)
		if len(pages) != 4 {
			t.Fatalf("reopen %d Page files = %d, want 4", attempt, len(pages))
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
