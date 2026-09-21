package sqlstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/sqlstore"
)

// A unit is identified by its number, and the number only means something
// together with the Database: every Database's units share one table.
func (h *harness) recallUnit(rowID string) (int64, string) {
	h.t.Helper()
	var unitNo int64
	var contentHash string
	if err := h.db.SQL().QueryRow(
		`SELECT unit_no, content_hash FROM mem_recall_units WHERE row_id = ?`, rowID).
		Scan(&unitNo, &contentHash); err != nil {
		h.t.Fatal(err)
	}
	return unitNo, contentHash
}

func (h *harness) storedVector(unitNo int64) (hash, model string, dimensions int, hasBytes bool) {
	h.t.Helper()
	var embeddedHash, embeddedModel string
	var embeddedDimensions int
	var bytes []byte
	if err := h.db.SQL().QueryRow(`SELECT embedded_content_hash, embedding_model, embedding_dimensions,
		embedding FROM mem_recall_units WHERE unit_no = ?`, unitNo).
		Scan(&embeddedHash, &embeddedModel, &embeddedDimensions, &bytes); err != nil {
		h.t.Fatal(err)
	}
	return embeddedHash, embeddedModel, embeddedDimensions, len(bytes) > 0
}

func TestVectorIdentityLocksOnFirstUseAndRejectsAnotherModel(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rowID := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	unitNo, contentHash := h.recallUnit(rowID)
	ctx := context.Background()

	identity, err := h.db.Rows().AcceptVector(ctx, "work", sqlstore.VectorRecord{
		UnitNo: unitNo, ContentHash: contentHash,
		Model: "text-embedding-v4", Dimensions: 3, Vector: []float32{1, 0, 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Model != "text-embedding-v4" || identity.Dimensions != 3 || identity.LockedAt == "" {
		t.Fatalf("the first accepted vector must lock the identity: %+v", identity)
	}
	if hash, model, dimensions, hasBytes := h.storedVector(unitNo); hash != contentHash ||
		model != "text-embedding-v4" || dimensions != 3 || !hasBytes {
		t.Fatalf("stored vector = %q/%s/%d bytes=%v", hash, model, dimensions, hasBytes)
	}

	// A different model is not a worse vector, it is an incomparable one, and
	// recall returns no scores — so a mixed index would be silently wrong.
	if _, err := h.db.Rows().AcceptVector(ctx, "work", sqlstore.VectorRecord{
		UnitNo: unitNo, ContentHash: contentHash,
		Model: "text-embedding-v3", Dimensions: 3, Vector: []float32{0, 1, 0},
	}); err == nil {
		t.Fatal("a second model must not be accepted into the same Database")
	}
	if _, err := h.db.Rows().AcceptVector(ctx, "work", sqlstore.VectorRecord{
		UnitNo: unitNo, ContentHash: contentHash,
		Model: "text-embedding-v4", Dimensions: 4, Vector: []float32{0, 1, 0, 0},
	}); err == nil {
		t.Fatal("the locked dimensions must be enforced")
	}
	// The rejections must leave the accepted vector exactly as it was.
	if hash, model, dimensions, hasBytes := h.storedVector(unitNo); hash != contentHash ||
		model != "text-embedding-v4" || dimensions != 3 || !hasBytes {
		t.Fatalf("a rejected vector changed the stored one: %q/%s/%d bytes=%v", hash, model, dimensions, hasBytes)
	}

	// A vector computed for other text describes a sentence this unit no longer
	// holds, and nothing downstream could notice.
	if _, err := h.db.Rows().AcceptVector(ctx, "work", sqlstore.VectorRecord{
		UnitNo: unitNo, ContentHash: "sha256:not-the-current-text",
		Model: "text-embedding-v4", Dimensions: 3, Vector: []float32{0, 0, 1},
	}); err == nil {
		t.Fatal("a vector for a different text must be rejected")
	}
}

func TestReadinessIsDerivedFromTheTruthColumns(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	first := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	h.insertAlongPath("write ahead log", pathOf("architecture", "wal"))
	ctx := context.Background()
	rows := h.db.Rows()

	if status, err := rows.VectorStatus(ctx, "work", ""); err != nil || status.NotReady != 2 {
		t.Fatalf("before any vector: %+v, %v", status, err)
	}
	unitNo, contentHash := h.recallUnit(first)
	if _, err := rows.AcceptVector(ctx, "work", sqlstore.VectorRecord{
		UnitNo: unitNo, ContentHash: contentHash,
		Model: "text-embedding-v4", Dimensions: 2, Vector: []float32{1, 0},
	}); err != nil {
		t.Fatal(err)
	}
	if status, err := rows.VectorStatus(ctx, "work", ""); err != nil || status.NotReady != 1 {
		t.Fatalf("after one vector: %+v, %v", status, err)
	}
	if status, err := rows.VectorStatus(ctx, "work", "notes"); err != nil || status.NotReady != 1 {
		t.Fatalf("the Table scope must agree: %+v, %v", status, err)
	}

	// Editing the text moves the content hash on, so the stored vector no longer
	// describes what the unit holds. Readiness is a predicate over the two
	// hashes, so nothing has to remember to mark it stale.
	refine := write("edit the text the vector was computed from")
	refine.ExpectedRevision = 1
	h.run(`UPDATE work.notes SET title = 'storage engine, reconsidered' WHERE row_id = :row`,
		map[string]any{"row": first}, refine)
	if status, err := rows.VectorStatus(ctx, "work", ""); err != nil || status.NotReady != 2 {
		t.Fatalf("an edited unit must be not-ready again: %+v, %v", status, err)
	}
	if hash, _, _, hasBytes := h.storedVector(unitNo); !hasBytes || hash == "" {
		t.Fatalf("the stale vector should still be readable, not erased: %q %v", hash, hasBytes)
	}

	// Re-embedding the current text makes it ready again.
	_, currentHash := h.recallUnit(first)
	if _, err := rows.AcceptVector(ctx, "work", sqlstore.VectorRecord{
		UnitNo: unitNo, ContentHash: currentHash,
		Model: "text-embedding-v4", Dimensions: 2, Vector: []float32{0, 1},
	}); err != nil {
		t.Fatal(err)
	}
	if status, err := rows.VectorStatus(ctx, "work", ""); err != nil || status.NotReady != 1 {
		t.Fatalf("after re-embedding: %+v, %v", status, err)
	}
}

func TestVectorIdentityBelongsToOneDatabase(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rowID := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	unitNo, contentHash := h.recallUnit(rowID)
	ctx := context.Background()

	// The harness scope covers "work" only, so the second Database needs an
	// authorization that names it.
	wide := authorization
	wide.AuthorizedDatabases = []string{"work", "other"}
	h.runAuthorized(wide, `CREATE DATABASE other PURPOSE 'Another domain' SCOPE 'Elsewhere'`, nil, executor.MutationOptions{})
	// Units from every Database share one table, so the lookup has to be scoped
	// by Database — a fitting number must not carry a vector across.
	if _, err := h.db.Rows().AcceptVector(ctx, "other", sqlstore.VectorRecord{
		UnitNo: unitNo, ContentHash: contentHash,
		Model: "text-embedding-v4", Dimensions: 3, Vector: []float32{1, 0, 0},
	}); err == nil {
		t.Fatal("a unit number must not resolve inside another Database")
	}

	if _, err := h.db.Rows().AcceptVector(ctx, "work", sqlstore.VectorRecord{
		UnitNo: unitNo, ContentHash: contentHash,
		Model: "text-embedding-v4", Dimensions: 3, Vector: []float32{1, 0, 0},
	}); err != nil {
		t.Fatal(err)
	}
	// The Database that accepted nothing is still unlocked and still has its own
	// readiness answer.
	if status, err := h.db.Rows().VectorStatus(ctx, "other", ""); err != nil || status.NotReady != 0 || status.IdentityLocked {
		t.Fatalf("the other Database owns no units and no identity: %+v, %v", status, err)
	}
	if status, err := h.db.Rows().VectorStatus(ctx, "work", ""); err != nil || status.NotReady != 0 || !status.IdentityLocked {
		t.Fatalf("the Database that accepted a vector is ready: %+v, %v", status, err)
	}
}

// A fresh Instance is created with the vector columns, so the only run that
// exercises the additive path is an Instance opened from before they existed.
// Reopen is the risky half of any schema change, so it is tested directly.
func TestAnInstanceFromBeforeTheVectorColumnsGainsThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memora.db")
	old, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
		CREATE TABLE mem_databases (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE COLLATE NOCASE,
			body TEXT NOT NULL
		);
		CREATE TABLE mem_recall_units (
			unit_no INTEGER PRIMARY KEY AUTOINCREMENT,
			route_id TEXT NOT NULL UNIQUE,
			database_id TEXT NOT NULL,
			table_id TEXT NOT NULL,
			row_id TEXT NOT NULL,
			revision INTEGER NOT NULL,
			content_hash TEXT NOT NULL,
			payload TEXT NOT NULL,
			payload_index TEXT NOT NULL,
			embedding_model TEXT NOT NULL DEFAULT '',
			embedding_dimensions INTEGER NOT NULL DEFAULT 0,
			embedded_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		);`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sqlstore.Open(path, sqlstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for table, columns := range map[string][]string{
		"mem_databases":    {"embedding_model", "embedding_dimensions", "embedding_locked_at"},
		"mem_recall_units": {"embedding", "embedded_content_hash"},
	} {
		for _, column := range columns {
			if !hHasColumn(t, db, table, column) {
				t.Fatalf("%s.%s was not added on open", table, column)
			}
		}
	}
}

func hHasColumn(t *testing.T, db *sqlstore.DB, table, column string) bool {
	t.Helper()
	rows, err := db.SQL().Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		name := ""
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return false
}

// A recall that could not draw on the vector path must say so. It returns paths
// and nothing else — no scores — so without this notice a partial answer is
// indistinguishable from a complete one.
func TestRecallReportsTheUnitsTheVectorPathCouldNotCover(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rowID := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	h.insertAlongPath("write ahead log", pathOf("architecture", "wal"))
	ctx := context.Background()

	partial := h.recallFrom(`RECALL FROM work MATCH :q LIMIT 5`, map[string]any{"q": "storage engine"})
	if len(partial.Warnings) != 1 {
		t.Fatalf("a recall over units without vectors must warn once: %+v", partial.Warnings)
	}
	notice := partial.Warnings[0]
	if notice.Code != result.CodeVectorsNotReady {
		t.Fatalf("notice code = %q", notice.Code)
	}
	if got := notice.Details["not_ready_units"]; got != 2 {
		t.Fatalf("not_ready_units = %v, want both units in scope", got)
	}
	if locked, present := notice.Details["identity_locked"]; !present || locked != false {
		t.Fatalf("an unlocked Database must say so: %+v", notice.Details)
	}
	// The health report carries the same number: the notice explains one answer,
	// the report is where you go to ask the question without a query in hand.
	if report := h.doctor(); report.UnitsWithoutVectors != 2 {
		t.Fatalf("doctor must count units without vectors: %+v", report)
	}

	// Cover one unit and the count in the notice follows the truth columns down.
	unitNo, contentHash := h.recallUnit(rowID)
	if _, err := h.db.Rows().AcceptVector(ctx, "work", sqlstore.VectorRecord{
		UnitNo: unitNo, ContentHash: contentHash,
		Model: "text-embedding-v4", Dimensions: 2, Vector: []float32{1, 0},
	}); err != nil {
		t.Fatal(err)
	}
	stillPartial := h.recallFrom(`RECALL FROM work MATCH :q LIMIT 5`, map[string]any{"q": "storage engine"})
	if len(stillPartial.Warnings) != 1 || stillPartial.Warnings[0].Details["not_ready_units"] != 1 {
		t.Fatalf("the notice must report the remaining unit: %+v", stillPartial.Warnings)
	}
	if locked := stillPartial.Warnings[0].Details["identity_locked"]; locked != true {
		t.Fatalf("the Database is locked now: %+v", stillPartial.Warnings[0].Details)
	}
	if report := h.doctor(); report.UnitsWithoutVectors != 1 {
		t.Fatalf("doctor must follow the truth columns: %+v", report)
	}
	// The notice explains the result; it never becomes part of it.
	if len(stillPartial.Rows) != 1 || len(partial.Rows) != 1 {
		t.Fatalf("warnings must not change the rows: %d then %d", len(partial.Rows), len(stillPartial.Rows))
	}
}
