package sqlstore_test

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

// A live Row must hang under exactly one leaf. Zero leaves is an unreachable
// Row — silently lost; two leaves breaks the one-to-one mount the semantic
// index is built on. Both are refused at commit, so nothing is half-written.

func TestInsertRequiresExactlyOneLeaf(t *testing.T) {
	h := newHarness(t)
	root, leaf := h.seedNotes()
	second := text(h.run(`CREATE ROUTE UNDER :p NAME 'second' KIND 'leaf' PURPOSE 'a second position, so a Row can be mounted twice by hand'`,
		map[string]any{"p": root}, write("second")).Rows[0]["route_id"])

	cases := []struct {
		name   string
		leaves []string
	}{
		{name: "no leaf"},
		{name: "two leaves", leaves: []string{leaf, second}},
	}
	for _, tc := range cases {
		mutation := write("reject")
		mutation.RouteLeafIDs = tc.leaves
		source := `INSERT INTO work.notes (title) VALUES ('rejected')`
		if code := h.fails(source, nil, mutation); code != result.CodeConstraint {
			t.Fatalf("%s: code = %s", tc.name, code)
		}
	}
	if h.liveRows() != 0 {
		t.Fatalf("a refused insert must write nothing: %d rows", h.liveRows())
	}
	if holder := h.leafHolder(leaf); holder != "" {
		t.Fatalf("a refused insert must not mount anything: leaf holds %q", holder)
	}

	// Naming the same leaf twice is one leaf, not two: the snapshot is deduped.
	duplicate := h.insertTitle("once", []string{leaf, leaf})
	if leaves := h.storedLeaves(duplicate); len(leaves) != 1 || leaves[0] != leaf {
		t.Fatalf("a duplicated leaf id must collapse: %v", leaves)
	}
}

func TestUpdateCannotLeaveALiveRowWithoutALeaf(t *testing.T) {
	h := newHarness(t)
	_, leaf := h.seedNotes()
	rowID := h.insertTitle("mounted", []string{leaf})

	clear := write("clear")
	clear.ExpectedRevision = 1
	clear.RouteLeafIDs = []string{}
	source := `UPDATE work.notes SET title = 'cleared' WHERE row_id = :row`
	if code := h.fails(source, map[string]any{"row": rowID}, clear); code != result.CodeConstraint {
		t.Fatalf("clearing the mount: code = %s", code)
	}

	if leaves := h.storedLeaves(rowID); len(leaves) != 1 || leaves[0] != leaf {
		t.Fatalf("a refused update must not change the mount: %v", leaves)
	}
	if holder := h.leafHolder(leaf); holder != rowID {
		t.Fatalf("a refused update must not unmount the leaf: %q", holder)
	}
	selected := h.run("SELECT * FROM `work`.`notes` WHERE row_id = :row LIMIT 1", map[string]any{"row": rowID}, executor.MutationOptions{})
	if len(selected.Rows) != 1 || text(selected.Rows[0]["title"]) != "mounted" {
		t.Fatalf("a refused update must not change the Row: %v", selected.Rows)
	}
}

// The mount option on UPDATE is the Row's complete membership, not an addition:
// naming another leaf moves the Row, and the leaf it left is free again.
func TestUpdateMountMovesTheRowInsteadOfAccumulating(t *testing.T) {
	h := newHarness(t)
	root, leaf := h.seedNotes()
	rowID := h.insertTitle("moves", []string{leaf})
	second := text(h.run(`CREATE ROUTE UNDER :p NAME 'second' KIND 'leaf' PURPOSE 'a second position, so a Row can be mounted twice by hand'`,
		map[string]any{"p": root}, write("second")).Rows[0]["route_id"])

	move := write("move")
	move.ExpectedRevision = 1
	move.RouteLeafIDs = []string{second}
	h.run(`UPDATE work.notes SET title = 'moved' WHERE row_id = :row`, map[string]any{"row": rowID}, move)

	if leaves := h.storedLeaves(rowID); len(leaves) != 1 || leaves[0] != second {
		t.Fatalf("stored leaves after the move = %v", leaves)
	}
	if holder := h.leafHolder(second); holder != rowID {
		t.Fatalf("the new leaf holds %q", holder)
	}
	if holder := h.leafHolder(leaf); holder != "" {
		t.Fatalf("the leaf the Row left must be free again, holds %q", holder)
	}

	reused := h.insertTitle("reuses the old leaf", []string{leaf})
	if holder := h.leafHolder(leaf); holder != reused {
		t.Fatalf("the freed leaf must accept a new Row: holds %q", holder)
	}
	opened := h.run(`OPEN ROUTE :leaf LIMIT 1`, map[string]any{"leaf": second}, executor.MutationOptions{})
	if len(opened.Rows) != 1 || text(opened.Rows[0]["row_id"]) != rowID {
		t.Fatalf("the moved Row must be reachable at its new leaf: %v", opened.Rows)
	}
}
