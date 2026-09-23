package sqlstore_test

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/recall"
	"github.com/HW-Yue/Memora/internal/result"
)

// A vector that cannot land must not be hidden from the caller. Found by the
// internals audit (docs/development/audit-2026-09-23.md, A2).

// A vector offered with a write is best effort: the Row is the fact and the
// vector is an index over it, so a vector that cannot land must not fail the
// write. What it must not do either is pretend it landed. The unit's own columns
// are what "ready" means, so they have to be written last: written first, the
// engine claimed a vector the index never received, `SHOW PENDING VECTORS`
// skipped the Row, and only doctor's byte-level drift could see it.
func TestABestEffortVectorThatCannotLandIsReportedNotHidden(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	first := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	h.acceptUnitVector(first, []float32{1, 0}) // locks the Database to two dimensions

	// An index three wide cannot hold a two-wide vector, and the registry still
	// says the index is there — a state a repaired-from-backup instance can be in.
	index := h.vectorIndexName()
	if _, err := h.db.SQL().Exec(`DROP TABLE ` + index); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SQL().Exec(`CREATE VIRTUAL TABLE ` + index + ` USING vec0(embedding float[3])`); err != nil {
		t.Fatal(err)
	}

	second := h.insertAlongPath("write ahead log", pathOf("architecture", "wal"))
	unitNo, contentHash := h.recallUnit(second)
	encoded, err := recall.EncodeVector([]float32{0, 1})
	if err != nil {
		t.Fatal(err)
	}
	mutation := write("update with a vector that cannot land")
	mutation.ExpectedRevision = 1
	mutation.Vector = &executor.VectorInput{Model: "text-embedding-v4", ContentHash: contentHash, Values: encoded}
	answer := h.run(`UPDATE work.notes SET title = :title WHERE row_id = :row`,
		map[string]any{"title": "write ahead log", "row": second}, mutation)

	// The fact is recorded anyway: best effort is not a failure of the write.
	if answer.Status != result.StatusSucceeded {
		t.Fatalf("a vector that cannot land must not fail the write: %v", answer.Error)
	}
	// The receipt says the vector did not land, so a caller cannot believe it did.
	warned := false
	for _, notice := range answer.Warnings {
		if strings.Contains(strings.ToLower(notice.Message), "vector") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("a write whose vector did not land must say so: %#v", answer.Warnings)
	}
	// And the unit keeps no vector, which is what puts it back in the pending
	// list for the host's next drain.
	hash, model, dimensions, hasBytes := h.storedVector(unitNo)
	if hasBytes || hash != "" || model != "" || dimensions != 0 {
		t.Fatalf("a vector that could not land left the unit claiming to be ready: %q/%s/%d bytes=%v",
			hash, model, dimensions, hasBytes)
	}
}
