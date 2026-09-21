package sqlstore_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/recall"
)

// unitCount asks the internal table directly, so the answer comes from this
// process's own connection rather than from the session that may be inside a
// transaction.
func (h *harness) unitCount(rowID string) int {
	h.t.Helper()
	count := 0
	if err := h.db.SQL().QueryRow(`SELECT COUNT(*) FROM mem_recall_units WHERE row_id = ?`, rowID).Scan(&count); err != nil {
		h.t.Fatal(err)
	}
	return count
}

// Writers are serialised and readers see the last commit (ADR-0011), so an
// in-flight write must be invisible everywhere: not as a Row, not as a recall
// hit, not as a unit, not as an index row. The interesting half is recall: it
// reads a derived index, and a reader that saw half of a write would see a
// position whose facts are not there yet.
func TestReadersSeeTheLastCommitAndRecallAgreesWithIt(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	ctx := context.Background()

	encoded, err := recall.EncodeVector([]float32{1, 0})
	if err != nil {
		t.Fatal(err)
	}
	options := write("write inside an explicit transaction")
	options.RoutePath = pathOf("architecture", "sqlite")
	options.Vector = &executor.VectorInput{
		Model: "text-embedding-v4", ContentHash: payloadHash("in flight"),
		Values: encoded,
	}

	h.run(`BEGIN`, nil, executor.MutationOptions{})
	inserted := h.run(`INSERT INTO work.notes (title) VALUES ('in flight')`, nil, options)
	rowID := text(inserted.Rows[0]["row_id"])

	// Inside the transaction the writer sees its own write — through the session
	// that owns the transaction, because that is the only connection that can.
	inside := h.run(`SELECT row_id FROM work.notes WHERE row_id = :row LIMIT 1`,
		map[string]any{"row": rowID}, executor.MutationOptions{})
	if len(inside.Rows) != 1 {
		t.Fatalf("the writer must see its own Row: %+v", inside.Rows)
	}

	// Outside it, nothing is visible — neither the unit, nor the position, nor a
	// pending-vector entry. unitCount reads on this process's own connection.
	if count := h.unitCount(rowID); count != 0 {
		t.Fatalf("an uncommitted unit must be invisible: %d", count)
	}
	if hits, err := h.db.Rows().RecallKeywords(ctx, "work", "", "in flight", 5); err != nil || len(hits) != 0 {
		t.Fatalf("recall must not see an uncommitted Row: %+v, %v", hits, err)
	}
	// The vector arm cannot even be asked yet: the Database's identity is locked
	// by the first accepted vector, and that acceptance is inside the open
	// transaction, so nothing about it has leaked out. Refusing is the honest
	// answer, and it is a different answer from "no hit".
	if _, err := h.db.Rows().RecallNearest(ctx, "work", "", []float32{1, 0}, 5); err == nil {
		t.Fatal("an uncommitted identity must not be visible to a vector query")
	}
	if status, err := h.db.Rows().VectorStatus(ctx, "work", ""); err != nil || status.NotReady != 0 {
		t.Fatalf("an uncommitted unit is not work a host can see: %+v, %v", status, err)
	}

	h.run(`COMMIT`, nil, executor.MutationOptions{})

	// At the commit boundary everything appears together, and the derived index
	// agrees with the truth it was committed with.
	if count := h.unitCount(rowID); count != 1 {
		t.Fatalf("the committed unit must be visible: %d", count)
	}
	if status, err := h.db.Rows().VectorStatus(ctx, "work", ""); err != nil || status.NotReady != 0 {
		t.Fatalf("the committed unit arrived ready: %+v, %v", status, err)
	}
	if hits, err := h.db.Rows().RecallKeywords(ctx, "work", "", "in flight", 5); err != nil || len(hits) != 1 {
		t.Fatalf("keyword recall must find it after the commit: %+v, %v", hits, err)
	}
	if hits, err := h.db.Rows().RecallNearest(ctx, "work", "", []float32{1, 0}, 5); err != nil || len(hits) != 1 {
		t.Fatalf("vector recall must find it after the commit: %+v, %v", hits, err)
	}
	if drift := h.doctor().VectorIndexDrift; drift != 0 {
		t.Fatalf("the index committed with the truth, so there is no drift: %d", drift)
	}
}

// A rolled-back write leaves nothing behind either: not a unit, not an index
// row, no pending work.
func TestARolledBackWriteLeavesNoTrace(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	encoded, err := recall.EncodeVector([]float32{1, 0})
	if err != nil {
		t.Fatal(err)
	}
	options := write("write and roll back")
	options.RoutePath = pathOf("architecture", "sqlite")
	options.Vector = &executor.VectorInput{
		Model: "text-embedding-v4", ContentHash: payloadHash("undone"), Values: encoded,
	}

	h.run(`BEGIN`, nil, executor.MutationOptions{})
	inserted := h.run(`INSERT INTO work.notes (title) VALUES ('undone')`, nil, options)
	rowID := text(inserted.Rows[0]["row_id"])
	h.run(`ROLLBACK`, nil, executor.MutationOptions{})

	// The Row never existed, so there is no unit to look for — and asking for
	// recall over the scope reports no pending work either.
	if hits, err := h.db.Rows().RecallKeywords(context.Background(), "work", "", "undone", 5); err != nil || len(hits) != 0 {
		t.Fatalf("a rolled-back Row is nowhere: %+v, %v", hits, err)
	}
	if status, err := h.db.Rows().VectorStatus(context.Background(), "work", ""); err != nil || status.NotReady != 0 {
		t.Fatalf("a rolled-back write leaves no work: %+v, %v", status, err)
	}
	if drift := h.doctor().VectorIndexDrift; drift != 0 {
		t.Fatalf("a rolled-back write leaves no index row: %d", drift)
	}
	_ = rowID
}
