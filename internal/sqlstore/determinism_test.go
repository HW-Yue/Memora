package sqlstore_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/HW-Yue/Memora/internal/sqlstore"
)

// Recall returns no scores, so a caller has no way to notice that the answer
// moved. Two runs of the same query over an unchanged instance must therefore
// agree completely — including which units survive a limit when distances tie,
// which is exactly the case an upstream ordering promise would not cover.
func TestVectorRecallIsIdenticalAcrossRunsWhenDistancesTie(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	first := h.insertAlongPath("first", pathOf("architecture", "sqlite"))
	second := h.insertAlongPath("second", pathOf("architecture", "wal"))
	third := h.insertAlongPath("third", pathOf("architecture", "btree"))
	for _, rowID := range []string{first, second, third} {
		h.acceptUnitVector(rowID, []float32{1, 0})
	}
	firstUnit, _ := h.recallUnit(first)

	for attempt := 0; attempt < 5; attempt++ {
		hits, err := h.db.Rows().RecallNearest(context.Background(), "work", "", []float32{1, 0}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 || hits[0].ObjectID != first {
			t.Fatalf("a tie must resolve the same way every time, got %+v", hits)
		}
		if unitNo, _ := h.recallUnit(hits[0].ObjectID); unitNo != firstUnit {
			t.Fatalf("the surviving unit must be the lowest number: %d", unitNo)
		}
	}
	// The same holds for the keyword arm, whose ordering is the same contract.
	query := map[string]any{"q": "first"}
	previous := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 5`, query)
	for attempt := 0; attempt < 3; attempt++ {
		current := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 5`, query)
		if len(current) != len(previous) {
			t.Fatalf("keyword recall changed between runs: %v then %v", previous, current)
		}
		for index := range previous {
			if previous[index] != current[index] {
				t.Fatalf("keyword recall changed between runs: %v then %v", previous, current)
			}
		}
	}
}

// Accepting the same embedding twice is not a second vector: the unit has one
// truth and one index row, so a retry (a client that did not see the first
// answer) cannot double anything.
func TestAcceptingTheSameVectorTwiceChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rowID := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	unitNo, contentHash := h.recallUnit(rowID)
	record := sqlstore.VectorRecord{
		UnitNo: unitNo, ContentHash: contentHash,
		Model: "text-embedding-v4", Dimensions: 2, Vector: []float32{1, 0},
	}
	ctx := context.Background()
	if _, err := h.db.Rows().AcceptVector(ctx, "work", record); err != nil {
		t.Fatal(err)
	}
	before := h.indexRows()
	if _, err := h.db.Rows().AcceptVector(ctx, "work", record); err != nil {
		t.Fatal(err)
	}
	after := h.indexRows()
	if len(before) != 1 || len(after) != 1 || before[0] != after[0] {
		t.Fatalf("a retry must not change the index: %v then %v", before, after)
	}
}

// The strongest form of "the index is derived": its contents after incremental
// upkeep are byte-for-byte what a full replay produces.
func TestARebuiltIndexIsByteForByteTheIncrementalOne(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rows := []string{
		h.insertAlongPath("first", pathOf("architecture", "sqlite")),
		h.insertAlongPath("second", pathOf("architecture", "wal")),
	}
	h.acceptUnitVector(rows[0], []float32{1, 0})
	h.acceptUnitVector(rows[1], []float32{0, 1})
	// An edit leaves a stale vector in place, so the rebuild has to reproduce
	// that too rather than quietly dropping it.
	refine := write("edit one Row so its vector goes stale")
	refine.ExpectedRevision = 1
	h.run(`UPDATE work.notes SET title = 'first, reconsidered' WHERE row_id = :row`,
		map[string]any{"row": rows[0]}, refine)
	incremental := h.indexRows()

	if _, err := h.db.SQL().Exec(`DROP TABLE ` + h.vectorIndexName()); err != nil {
		t.Fatal(err)
	}
	if repaired := h.repairVectorIndex(`REPAIR VECTOR INDEX IN DATABASE work LIMIT 100`); repaired != 2 {
		t.Fatalf("the replay must cover every unit that holds bytes: %d", repaired)
	}
	rebuilt := h.indexRows()
	if len(incremental) != len(rebuilt) {
		t.Fatalf("rebuild produced %d rows, incremental upkeep %d", len(rebuilt), len(incremental))
	}
	for index := range incremental {
		if incremental[index] != rebuilt[index] {
			t.Fatalf("row %d differs: %s then %s", index, incremental[index], rebuilt[index])
		}
	}
}

// indexRows reads the derived index as bytes, so two states can be compared
// without going through a query whose ordering we would then also be trusting.
func (h *harness) indexRows() []string {
	h.t.Helper()
	rows, err := h.db.SQL().Query(`SELECT rowid, hex(embedding) FROM ` + h.vectorIndexName() + ` ORDER BY rowid`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	dump := []string{}
	for rows.Next() {
		var rowid int64
		var embedding string
		if err := rows.Scan(&rowid, &embedding); err != nil {
			h.t.Fatal(err)
		}
		dump = append(dump, strconv.FormatInt(rowid, 10)+":"+embedding)
	}
	if err := rows.Err(); err != nil {
		h.t.Fatal(err)
	}
	return dump
}
