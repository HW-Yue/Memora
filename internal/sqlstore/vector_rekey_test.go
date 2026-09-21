package sqlstore_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/recall"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/sqlstore"
)

// A Database commits to one (model, dimensions) pair the first time it accepts a
// vector. That commitment is what makes recall comparable, and it is also a trap:
// a trial model, or another host's default provider, pins the Database for good.
// A rekey is the way out, and it has two properties the rest of the vector layer
// depends on:
//
//   - it is bounded and repeatable, like every other pass in this family, so a
//     Database with thousands of units does not have to be rewritten in one
//     statement;
//   - while it runs, no other vector path may read or write half-way truth — a
//     reconcile pass in that window would rebuild the index out of the bytes the
//     rekey is removing.

func (h *harness) tableID(name string) string {
	h.t.Helper()
	var id string
	if err := h.db.SQL().QueryRow(`SELECT id FROM mem_tables WHERE name = ?`, name).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	return id
}

// vecIndexDDL is the derived index's own declaration, so a test can see the
// dimension it was welded to (float[N] is fixed when the virtual table is made).
func (h *harness) vecIndexDDL(tableID string) string {
	h.t.Helper()
	var ddl string
	err := h.db.SQL().QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`,
		"mem_recall_vec_"+tableID).Scan(&ddl)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return ddl
}

func (h *harness) vecIndexRegistry(tableID string) (string, int, bool) {
	h.t.Helper()
	var model string
	var dimensions int
	err := h.db.SQL().QueryRow(`SELECT model, dimensions FROM mem_recall_vec_indexes WHERE table_id = ?`,
		tableID).Scan(&model, &dimensions)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return model, dimensions, true
}

func requireRekeyCode(t *testing.T, err error, want result.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("the vector layer must refuse during a rekey, want %s", want)
	}
	var failure *sqlstore.Error
	if !errors.As(err, &failure) {
		t.Fatalf("error is not a storage error: %v", err)
	}
	if failure.Code != want {
		t.Fatalf("code = %s, want %s (%s)", failure.Code, want, failure.Message)
	}
}

func TestRekeyMovesTheIdentityInBoundedPasses(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	ctx := context.Background()
	rows := h.db.Rows()
	tableID := h.tableID("notes")

	first := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	second := h.insertAlongPath("write ahead log", pathOf("architecture", "wal"))
	firstUnit, firstHash := h.recallUnit(first)
	secondUnit, secondHash := h.recallUnit(second)
	for unit, hash := range map[int64]string{firstUnit: firstHash, secondUnit: secondHash} {
		if _, err := rows.AcceptVector(ctx, "work", sqlstore.VectorRecord{
			UnitNo: unit, ContentHash: hash,
			Model: "m-a", Dimensions: 3, Vector: []float32{1, 0, 0},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if ddl := h.vecIndexDDL(tableID); !strings.Contains(ddl, "float[3]") {
		t.Fatalf("the locked identity must weld the index to 3 dimensions: %q", ddl)
	}

	// One pass releases at most LIMIT units, so the caller can repeat it instead
	// of rewriting the whole Database in a single statement.
	progress, err := rows.RekeyVectorIdentity(ctx, "work", 1,
		&sqlstore.VectorRekeyTarget{Model: "m-b", Dimensions: 4})
	if err != nil {
		t.Fatal(err)
	}
	if progress.Released != 1 || progress.Remaining != 1 || !progress.Rekeying {
		t.Fatalf("first pass = %+v, want one released, one remaining, still rekeying", progress)
	}
	if ddl := h.vecIndexDDL(tableID); ddl != "" {
		t.Fatalf("the derived index must be dropped with the identity it was built for: %q", ddl)
	}
	if _, _, found := h.vecIndexRegistry(tableID); found {
		t.Fatal("the registry row must go with the index it describes")
	}

	// The window is the dangerous part. Nothing may read or write a half-way
	// truth: not a new vector, not a reconcile pass, and not a vector query.
	_, err = rows.AcceptVector(ctx, "work", sqlstore.VectorRecord{
		UnitNo: secondUnit, ContentHash: secondHash,
		Model: "m-b", Dimensions: 4, Vector: []float32{1, 0, 0, 0},
	})
	requireRekeyCode(t, err, result.CodeRekeyInProgress)
	_, err = rows.RecallNearest(ctx, "work", "", []float32{1, 0, 0}, 5)
	requireRekeyCode(t, err, result.CodeRekeyInProgress)
	_, err = rows.RepairVectorIndex(ctx, "work", 10)
	requireRekeyCode(t, err, result.CodeRekeyInProgress)
	_, err = rows.PendingVectors(ctx, "work", 10)
	requireRekeyCode(t, err, result.CodeRekeyInProgress)

	// Status still answers, and it is the one place that has to name the target:
	// a host reading it has to know which identity the drain will want.
	status, err := rows.VectorStatus(ctx, "work", "")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Rekeying || status.Model != "m-b" || status.Dimensions != 4 {
		t.Fatalf("status during a rekey = %+v", status)
	}

	progress, err = rows.RekeyVectorIdentity(ctx, "work", 1,
		&sqlstore.VectorRekeyTarget{Model: "m-b", Dimensions: 4})
	if err != nil {
		t.Fatal(err)
	}
	if progress.Released != 1 || progress.Remaining != 0 || progress.Rekeying {
		t.Fatalf("final pass = %+v, want nothing remaining and the window closed", progress)
	}
	if progress.Model != "m-b" || progress.Dimensions != 4 {
		t.Fatalf("the Database must end locked to the target: %+v", progress)
	}

	// The window is closed, so the ordinary paths work again — and the next
	// accepted vector rebuilds the index at the target's dimensions.
	if _, err := rows.RepairVectorIndex(ctx, "work", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := rows.AcceptVector(ctx, "work", sqlstore.VectorRecord{
		UnitNo: firstUnit, ContentHash: firstHash,
		Model: "m-b", Dimensions: 4, Vector: []float32{1, 0, 0, 0},
	}); err != nil {
		t.Fatal(err)
	}
	if ddl := h.vecIndexDDL(tableID); !strings.Contains(ddl, "float[4]") {
		t.Fatalf("the rebuilt index must use the new dimensions: %q", ddl)
	}
	if model, dimensions, found := h.vecIndexRegistry(tableID); !found || model != "m-b" || dimensions != 4 {
		t.Fatalf("registry = %s/%d found=%v", model, dimensions, found)
	}
}

func TestRekeyWithoutATargetLeavesTheDatabaseUnlocked(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	ctx := context.Background()
	rows := h.db.Rows()

	rowID := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	unitNo, contentHash := h.recallUnit(rowID)
	if _, err := rows.AcceptVector(ctx, "work", sqlstore.VectorRecord{
		UnitNo: unitNo, ContentHash: contentHash,
		Model: "m-a", Dimensions: 3, Vector: []float32{1, 0, 0},
	}); err != nil {
		t.Fatal(err)
	}

	// No target means the Database comes out with no identity at all, which is
	// the case a user hits when the provider they configured first was a trial:
	// whatever they configure next has to be free to lock it again.
	progress, err := rows.RekeyVectorIdentity(ctx, "work", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Rekeying || progress.Model != "" || progress.Dimensions != 0 || progress.Remaining != 0 {
		t.Fatalf("releasing without a target = %+v", progress)
	}
	status, err := rows.VectorStatus(ctx, "work", "")
	if err != nil {
		t.Fatal(err)
	}
	if status.IdentityLocked || status.Rekeying {
		t.Fatalf("the Database must be unlocked: %+v", status)
	}

	if _, err := rows.AcceptVector(ctx, "work", sqlstore.VectorRecord{
		UnitNo: unitNo, ContentHash: contentHash,
		Model: "m-b", Dimensions: 5, Vector: []float32{1, 0, 0, 0, 0},
	}); err != nil {
		t.Fatalf("an unlocked Database must accept the next identity: %v", err)
	}
	status, err = rows.VectorStatus(ctx, "work", "")
	if err != nil {
		t.Fatal(err)
	}
	if !status.IdentityLocked || status.Model != "m-b" || status.Dimensions != 5 {
		t.Fatalf("status after the new lock = %+v", status)
	}
}

// reopen closes this Instance and opens it again, so a test can watch what the
// window looks like after a restart rather than only what the same process
// remembers.
func (h *harness) reopen() {
	h.t.Helper()
	if err := h.db.Close(); err != nil {
		h.t.Fatal(err)
	}
	db, err := sqlstore.Open(h.path, sqlstore.Options{CheckInvariants: true})
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = db.Close() })
	h.db = db
}

// A rekey window has to survive a restart: it is the one state where the derived
// index and the identity disagree on purpose, so a crash that lost it would
// leave a Database whose index is missing while nothing knows to rebuild it.
func TestRekeyWindowSurvivesAReopen(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	ctx := context.Background()
	rows := h.db.Rows()

	first := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	second := h.insertAlongPath("write ahead log", pathOf("architecture", "wal"))
	firstUnit, firstHash := h.recallUnit(first)
	secondUnit, secondHash := h.recallUnit(second)
	for unit, hash := range map[int64]string{firstUnit: firstHash, secondUnit: secondHash} {
		if _, err := rows.AcceptVector(ctx, "work", sqlstore.VectorRecord{
			UnitNo: unit, ContentHash: hash,
			Model: "m-a", Dimensions: 3, Vector: []float32{1, 0, 0},
		}); err != nil {
			t.Fatal(err)
		}
	}
	progress, err := rows.RekeyVectorIdentity(ctx, "work", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Rekeying || progress.Remaining != 1 {
		t.Fatalf("the first pass must leave the window open: %+v", progress)
	}

	h.reopen()
	rows = h.db.Rows()
	status, err := rows.VectorStatus(ctx, "work", "")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Rekeying {
		t.Fatalf("the window must outlive the process that opened it: %+v", status)
	}
	if _, err := rows.AcceptVector(ctx, "work", sqlstore.VectorRecord{
		UnitNo: firstUnit, ContentHash: firstHash,
		Model: "m-b", Dimensions: 3, Vector: []float32{0, 1, 0},
	}); err == nil {
		t.Fatal("a reopened Instance is still inside the window")
	}

	progress, err = rows.RekeyVectorIdentity(ctx, "work", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Rekeying || progress.Remaining != 0 {
		t.Fatalf("the reopened Instance must be able to finish the rekey: %+v", progress)
	}
	status, err = rows.VectorStatus(ctx, "work", "")
	if err != nil {
		t.Fatal(err)
	}
	if status.Rekeying || status.IdentityLocked {
		t.Fatalf("after finishing, the Database is unlocked and open for business: %+v", status)
	}
}

// The statement is the surface a host actually uses, so its own contract is
// checked here: structural (never a quiet write), bounded by max_affected_rows,
// and honest about the window.
func TestRekeyStatementIsStructuralAndBounded(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	ctx := context.Background()
	first := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	second := h.insertAlongPath("write ahead log", pathOf("architecture", "wal"))
	firstUnit, firstHash := h.recallUnit(first)
	secondUnit, secondHash := h.recallUnit(second)
	for unit, hash := range map[int64]string{firstUnit: firstHash, secondUnit: secondHash} {
		if _, err := h.db.Rows().AcceptVector(ctx, "work", sqlstore.VectorRecord{
			UnitNo: unit, ContentHash: hash,
			Model: "m-a", Dimensions: 3, Vector: []float32{1, 0, 0},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// A write-level session may not rekey: the pass drops derived indexes, and a
	// vec0 table's width is welded into its declaration.
	writeLevel := authorization
	writeLevel.DefaultLevel = security.LevelWrite
	if code := h.failsAuthorized(writeLevel, `REKEY VECTOR IDENTITY IN DATABASE work LIMIT 1`, nil, write("rekey")); code != result.CodePermissionDenied {
		t.Fatalf("a write-level session rekeying = %s, want %s", code, result.CodePermissionDenied)
	}

	budget := write("rekey more units than the budget allows")
	budget.MaxAffectedRows = 1
	if code := h.fails(`REKEY VECTOR IDENTITY IN DATABASE work LIMIT 8`, nil, budget); code != result.CodeValidation {
		t.Fatalf("over-budget rekey = %s, want %s", code, result.CodeValidation)
	}
	// A target is a pair: the grammar refuses half of one rather than leaving it
	// to the executor, so the caller learns which token is missing.
	if code := h.fails(`REKEY VECTOR IDENTITY IN DATABASE work LIMIT 1 MODEL 'm-b'`, nil, write("half a target")); code != result.CodeParseError {
		t.Fatalf("a target without dimensions = %s, want %s", code, result.CodeParseError)
	}

	// One pass releases one unit and leaves the window open, naming the identity
	// the Database will end up on.
	firstPass := h.run(`REKEY VECTOR IDENTITY IN DATABASE work LIMIT 1 MODEL 'm-b' DIMENSIONS 4`, nil,
		write("move to another identity"))
	row := firstPass.Rows[0]
	if text(row["released"]) != "1" || text(row["remaining"]) != "1" || text(row["rekeying"]) != "true" {
		t.Fatalf("first pass = %v", row)
	}
	if text(row["model"]) != "m-b" || text(row["dimensions"]) != "4" {
		t.Fatalf("the receipt must name the target: %v", row)
	}

	// Inside the window a vector query is refused with its own code, not answered
	// with an empty page: "no matches" and "cannot answer" are different answers
	// and only one of them is true.
	encoded, err := recall.EncodeVector([]float32{1, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	if code := h.fails(`RECALL FROM work NEAREST :v LIMIT 5`, map[string]any{"v": encoded}, executor.MutationOptions{}); code != result.CodeRekeyInProgress {
		t.Fatalf("a vector query inside the window = %s, want %s", code, result.CodeRekeyInProgress)
	}

	// Repeating the same statement — with the target — is what finishes the
	// move; dropping the target is the escape hatch, not the continuation.
	final := h.run(`REKEY VECTOR IDENTITY IN DATABASE work LIMIT 1 MODEL 'm-b' DIMENSIONS 4`, nil,
		write("release the last unit"))
	row = final.Rows[0]
	if text(row["released"]) != "1" || text(row["remaining"]) != "0" || text(row["rekeying"]) != "false" {
		t.Fatalf("final pass = %v", row)
	}
	if text(row["model"]) != "m-b" || text(row["dimensions"]) != "4" {
		t.Fatalf("the Database must be locked to the target: %v", row)
	}
	if _, err := h.db.Rows().AcceptVector(ctx, "work", sqlstore.VectorRecord{
		UnitNo: secondUnit, ContentHash: secondHash,
		Model: "m-b", Dimensions: 4, Vector: []float32{1, 0, 0, 0},
	}); err != nil {
		t.Fatalf("the new identity must be usable: %v", err)
	}
}

// A window nobody finishes would leave the Database refusing every vector
// answer with no way out, so a pass without a target is also the escape hatch:
// it re-aims the open window at unlocked.
func TestRekeyWindowCanBeAbandonedIntoAnUnlockedDatabase(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	ctx := context.Background()
	rows := h.db.Rows()

	first := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	second := h.insertAlongPath("write ahead log", pathOf("architecture", "wal"))
	firstUnit, firstHash := h.recallUnit(first)
	secondUnit, secondHash := h.recallUnit(second)
	for unit, hash := range map[int64]string{firstUnit: firstHash, secondUnit: secondHash} {
		if _, err := rows.AcceptVector(ctx, "work", sqlstore.VectorRecord{
			UnitNo: unit, ContentHash: hash,
			Model: "m-a", Dimensions: 3, Vector: []float32{1, 0, 0},
		}); err != nil {
			t.Fatal(err)
		}
	}

	progress, err := rows.RekeyVectorIdentity(ctx, "work", 1,
		&sqlstore.VectorRekeyTarget{Model: "m-b", Dimensions: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Rekeying || progress.Remaining != 1 {
		t.Fatalf("the window must be open and aimed: %+v", progress)
	}
	if _, err := rows.RekeyVectorIdentity(ctx, "work", 1,
		&sqlstore.VectorRekeyTarget{Model: "m-c", Dimensions: 5}); err == nil {
		t.Fatal("an open window may not be re-aimed at another identity")
	}

	// Abandon it: no target means the Database comes out with no identity, and
	// the units already released stay released.
	progress, err = rows.RekeyVectorIdentity(ctx, "work", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Rekeying || progress.Remaining != 0 || progress.Model != "" || progress.Dimensions != 0 {
		t.Fatalf("abandoning = %+v", progress)
	}
	if _, err := rows.AcceptVector(ctx, "work", sqlstore.VectorRecord{
		UnitNo: secondUnit, ContentHash: secondHash,
		Model: "m-c", Dimensions: 5, Vector: []float32{1, 0, 0, 0, 0},
	}); err != nil {
		t.Fatalf("after abandoning, the next configuration must lock freely: %v", err)
	}
}

// A window refuses the vector path, but it is not a broken Instance: keyword
// recall still answers, doctor still reports, and neither calls the deliberately
// missing index corruption.
func TestRekeyWindowLeavesTheRestOfTheInstanceUsable(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	ctx := context.Background()
	rows := h.db.Rows()

	first := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	second := h.insertAlongPath("write ahead log", pathOf("architecture", "wal"))
	firstUnit, firstHash := h.recallUnit(first)
	secondUnit, secondHash := h.recallUnit(second)
	for unit, hash := range map[int64]string{firstUnit: firstHash, secondUnit: secondHash} {
		if _, err := rows.AcceptVector(ctx, "work", sqlstore.VectorRecord{
			UnitNo: unit, ContentHash: hash,
			Model: "m-a", Dimensions: 3, Vector: []float32{1, 0, 0},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := rows.RekeyVectorIdentity(ctx, "work", 1, nil); err != nil {
		t.Fatal(err)
	}

	// Keyword recall does not need a vector, so it keeps answering — and it says
	// out loud that the other arm could not have.
	page := h.run(`RECALL FROM work MATCH 'storage' LIMIT 5`, nil, executor.MutationOptions{})
	if len(page.Rows) == 0 {
		t.Fatal("keyword recall must keep working during a rekey")
	}
	noticed := false
	for _, warning := range page.Warnings {
		if warning.Code == result.CodeVectorsNotReady {
			noticed = true
			if warning.Details["rekeying"] != true || warning.Details["rekey_remaining"] != 1 {
				t.Fatalf("a notice from inside a window must describe it: %+v", warning.Details)
			}
		}
	}
	if !noticed {
		t.Fatalf("a keyword answer inside a window must carry the readiness notice: %+v", page.Warnings)
	}

	// Doctor has to be able to look at the Instance it is asked about — and to
	// say which Database is between identities rather than reporting the
	// deliberately missing index as drift.
	report := h.doctor()
	if report.RekeyingDatabases != 1 {
		t.Fatalf("doctor's rekey count = %d, want 1", report.RekeyingDatabases)
	}
	if report.VectorIndexDrift != 0 {
		t.Fatalf("a window is not drift: %d", report.VectorIndexDrift)
	}
	if report.Status != "healthy" {
		t.Fatalf("a rekey is work, not a fault: %+v", report)
	}
}
