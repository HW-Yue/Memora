package pagestoremigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	"github.com/HW-Yue/Memora/internal/store/rowversionindex"
	"github.com/HW-Yue/Memora/internal/store/wal"
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

// TestOneRowsHistoryIsOneRangeScanInItsTable is E5 stage 4's decisive gate.
//
// The property is not "the revisions can be found" — they always could, one
// point lookup at a time. It is that a Table's revisions are a contiguous
// segment of that Table's own Tree: reading a Row's whole history is a range
// scan, and another Table's revisions are not merely filtered out of it, they
// are not in the Tree at all.
//
// See docs/storage/per-table-tree-v1.md §4.
func TestOneRowsHistoryIsOneRangeScanInItsTable(t *testing.T) {
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
	note, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "first"}, row.WriteOptions{
		ExpectedSchemaVersion: notes.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	latest := note
	for _, title := range []string{"second", "third"} {
		latest, err = rows.Update(ctx, "work", "notes", note.ID, map[string]any{"title": title}, row.WriteOptions{
			ExpectedSchemaVersion: notes.SchemaVersion, ExpectedRevision: latest.Revision,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	task, err := rows.Insert(ctx, "work", "tasks", map[string]any{"title": "a task"}, row.WriteOptions{
		ExpectedSchemaVersion: tasks.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}

	history := authority.generation.HistoryFor(notes.ID)
	if history == nil {
		t.Fatalf("Table %q has no history Index", notes.ID)
	}
	// One range scan, newest first, returning every revision of this Row and
	// nothing else.
	page, err := history.RevisionsPage(note.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Locators) != 3 {
		t.Fatalf("history range scan = %#v, want 3 revisions", page.Locators)
	}
	for index, locator := range page.Locators {
		wantRevision := uint64(3 - index)
		if locator.RowID != note.ID || locator.Revision != wantRevision || locator.TableID != notes.ID {
			t.Fatalf("revision %d = %#v, want Row %q revision %d", index, locator, note.ID, wantRevision)
		}
	}
	// The decisive half: the other Table's revisions are not in this Tree.
	if _, err := history.ByRevision(task.ID, 1); !errors.Is(err, rowversionindex.ErrNotFound) {
		t.Fatalf("Table %q history found %q, error = %v", notes.ID, task.ID, err)
	}
	if _, err := authority.generation.HistoryFor(tasks.ID).ByRevision(task.ID, 1); err != nil {
		t.Fatalf("Table %q history is missing its own Row: %v", tasks.ID, err)
	}
}

// TestATableMissingOnlyItsHistoryTreeGrowsOne covers the upgrade every existing
// database takes: a v5 generation written before history became a Table has the
// clustered Tree and nothing else.
//
// EnsureTableTrees used to decide a Table was done by looking at one Tree, so
// such a Table was skipped forever and every publication into it failed for
// want of a history Tree. Completeness is per Tree, not per Table.
//
// The fixture builds the old layout rather than deleting a Tree from the new
// one: the shared redo log of a generation that once had a history Tree still
// carries that space's Records, so removing the Tree afterwards produces a
// database no version of this code ever wrote.
func TestATableMissingOnlyItsHistoryTreeGrowsOne(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	plan := currentPlan(t, file)
	buildPreHistoryGeneration(t, directory, plan)

	generation, err := openLiveGeneration(filepath.Join(directory, GenerationDirectory))
	if err != nil {
		t.Fatalf("a pre-history generation must still open: %v", err)
	}
	t.Cleanup(func() { _ = generation.Close() })
	if !generation.TableTree(table.ID) || generation.HistoryTree(table.ID) {
		t.Fatalf("fixture is not the pre-history layout for %q", table.ID)
	}

	if err := generation.EnsureTableTrees([]string{table.ID}); err != nil {
		t.Fatalf("EnsureTableTrees on a half-built Table: %v", err)
	}
	if !generation.HistoryTree(table.ID) {
		t.Fatalf("Table %q kept its clustered Tree and never grew a history Tree", table.ID)
	}
	if !generation.TableTree(table.ID) {
		t.Fatalf("Table %q lost its clustered Tree", table.ID)
	}
}

// buildPreHistoryGeneration writes a v5 generation the way the applier did
// before a Table had a history Tree: the three fixed Trees, then one clustered
// Tree per Table and nothing else.
func buildPreHistoryGeneration(t *testing.T, directory string, plan Plan) {
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
	log, err := wal.CreateSegmentSet(filepath.Join(target, sharedWALDirectory), 0)
	if err != nil {
		t.Fatal(err)
	}
	manifest := generationManifest{
		Version: generationVersion, PlanVersion: PlanVersion,
		PlanDigest: plan.Digest, SourceFingerprint: plan.SourceFingerprint,
	}
	specifications := append([]treeManifest(nil), expectedTrees...)
	for _, tableID := range planTableIDs(plan) {
		specifications = append(specifications, tableTreeManifest(tableID))
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

// TestWritesNoLongerProduceHistoryRecords is E5 stage 5's gate.
//
// A Row's history has two halves: the revisions, which live in the Table's
// history Tree, and the attribution, which belongs to the transaction that
// wrote them and lives once per transaction in the Change Log. The per-Row
// History record (nativestore.ObjectKindHistory) duplicated the second half
// once per revision, and nothing reads it any more except as a fallback for
// revisions written before the Change Log carried attribution.
//
// So new writes stop producing it. Old records stay readable — a Database that
// holds them still answers SHOW HISTORY from them, which is what the fallback
// is for.
//
// The gate is the pair: no new record, and the answer does not change.
func TestWritesNoLongerProduceHistoryRecords(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, notes, _ := authorityValuesWithoutRow(t, ctx, file, authority)

	before, err := file.IDs(nativestore.ObjectKindHistory)
	if err != nil {
		t.Fatal(err)
	}
	note, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "first"}, row.WriteOptions{
		ExpectedSchemaVersion: notes.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Update(ctx, "work", "notes", note.ID, map[string]any{"title": "second"}, row.WriteOptions{
		ExpectedSchemaVersion: notes.SchemaVersion, ExpectedRevision: note.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	after, err := file.IDs(nativestore.ObjectKindHistory)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("two writes produced %d new History records", len(after)-len(before))
	}

	// And the answer is unchanged: both revisions, newest first, each carrying
	// the attribution of the transaction that wrote it.
	records, err := authority.History(ctx, notes, note.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("SHOW HISTORY = %#v, want 2 revisions", records)
	}
	for index, record := range records {
		wantRevision := uint64(2 - index)
		if record.Revision != wantRevision || record.RowID != note.ID ||
			record.Actor == "" || record.Reason == "" {
			t.Fatalf("revision %d = %#v", index, record)
		}
	}
	if records[0].Operation != history.OperationUpdate || records[1].Operation != history.OperationInsert {
		t.Fatalf("operations = %q / %q", records[0].Operation, records[1].Operation)
	}
}
