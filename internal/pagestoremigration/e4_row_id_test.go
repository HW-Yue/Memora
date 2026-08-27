package pagestoremigration

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/rowid"
)

// TestRowIDsCountUpPerTableAndSurviveReopen is E4 stage 3's gate.
//
// Row IDs used to be UUIDs from a global source, so nothing about an ID said
// which Row of its Table it was. They now count up within the Table, from a
// counter held in that Table's own Tree and advanced in the same commit as the
// Row that took the number — so an ID cannot be handed out twice, and a crash
// between allocation and commit leaves a gap rather than a repeat.
//
// See docs/storage/per-table-tree-v1.md §3.
func TestRowIDsCountUpPerTableAndSurviveReopen(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	dictionary, rows, notes, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	tasks, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "tasks", Purpose: "Tasks", RowSemantics: "One task",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT", Purpose: "Title"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	insert := func(table catalog.Table, title string) row.Row {
		t.Helper()
		value, err := rows.Insert(ctx, "work", table.Name, map[string]any{"title": title}, row.WriteOptions{
			ExpectedSchemaVersion: table.SchemaVersion,
		})
		if err != nil {
			t.Fatalf("insert into %s: %v", table.Name, err)
		}
		return value
	}

	// Two Tables numbering independently: interleaving inserts must not make
	// either sequence skip.
	first, second := insert(notes, "first note"), insert(tasks, "first task")
	third, fourth := insert(notes, "second note"), insert(tasks, "second task")
	for _, want := range []struct {
		value  row.Row
		number uint64
	}{{first, 1}, {second, 1}, {third, 2}, {fourth, 2}} {
		number, ok := rowid.Number(want.value.ID)
		if !ok || number != want.number {
			t.Fatalf("Row %q number = %d, %v; want %d", want.value.ID, number, ok, want.number)
		}
	}
	// Per-Table numbering, globally unique IDs: same number, different Table,
	// different ID. Everything that refers to a Row across Tables — the change
	// log, Relations, a leaf's RowID — keeps working unchanged.
	if first.ID == second.ID {
		t.Fatalf("two Tables' first Rows share the ID %q", first.ID)
	}

	// Reopening must not rewind the counter: the next insert continues rather
	// than reusing a number.
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAuthority(ctx, file, directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	next, err := reopened.NextRowID(ctx, notes.DatabaseID, notes.ID)
	if err != nil {
		t.Fatal(err)
	}
	if number, ok := rowid.Number(next); !ok || number != 3 {
		t.Fatalf("after reopen the next Row ID is %q (number %d), want number 3", next, number)
	}
}
