package daemon

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
)

// rowArchiveFixture builds two Routed Rows and one Relation between them.
func rowArchiveFixture(t *testing.T) (string, string, string, string, string) {
	t.Helper()
	dataDir := archiveInstance(t)
	rootResult := executeTraceMSQL(t, dataDir,
		"CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE :purpose",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"purpose": "Notes root"}},
			Mutation:   executor.MutationOptions{MaxAffectedRows: 1},
		}},
	)
	rootID, _ := rootResult.Results[0].Rows[0]["route_id"].(string)
	leafID := func(name string) string {
		t.Helper()
		created := executeTraceMSQL(t, dataDir,
			"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose",
			[]executor.StatementInput{{
				Parameters: executor.Parameters{Named: map[string]any{
					"parent": rootID, "name": name, "kind": "leaf", "purpose": name + " Row",
				}},
				Mutation: executor.MutationOptions{MaxAffectedRows: 1},
			}},
		)
		id, _ := created.Results[0].Rows[0]["route_id"].(string)
		return id
	}
	first, second := leafID("first"), leafID("second")

	existing := executeTraceMSQL(t, dataDir, "SELECT row_id FROM work.notes LIMIT 10", nil)
	sourceRow, _ := existing.Results[0].Rows[0]["row_id"].(string)
	executeTraceMSQL(t, dataDir,
		"UPDATE work.notes SET title = :title WHERE row_id = :row",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"title": "first", "row": sourceRow}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
				RouteLeafIDs: []string{first},
				Actor:        "agent:test", Source: "event_1b", Reason: "attach to a leaf",
			},
		}},
	)
	inserted := executeTraceMSQL(t, dataDir,
		"INSERT INTO work.notes (title) VALUES (:title)",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"title": "second"}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:test",
				Source: "event_2", Reason: "relation fixture", RouteLeafIDs: []string{second},
			},
		}},
	)
	targetRow, _ := inserted.Results[0].Rows[0]["row_id"].(string)

	related := executeTraceMSQL(t, dataDir,
		"RELATE work.notes ROW :source TO work.notes ROW :target TYPE :kind",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"source": sourceRow, "target": targetRow, "kind": "supersedes",
			}},
			Mutation: executor.MutationOptions{
				MaxAffectedRows: 1, Actor: "agent:test", Source: "event_3", Reason: "relation fixture",
			},
		}},
	)
	relationID, _ := related.Results[0].Rows[0]["relation_id"].(string)
	return dataDir, sourceRow, relationID, first, rootID
}

// TestDeletingARowIsFinalAndTakesItsHistory pins the rule that separates UPDATE
// from DELETE: an UPDATE keeps every earlier revision, because the Row is still
// there to reach them through. A deleted Row is reachable from nothing —
// SHOW HISTORY is addressable only by RowID, and a deleted Row appears in no
// listing — so keeping its History would only mean content the user deleted
// stays queryable by whoever kept the ID.
func TestDeletingARowIsFinalAndTakesItsHistory(t *testing.T) {
	dataDir, rowID, _, _, _ := rowArchiveFixture(t)

	history := executeTraceMSQL(t, dataDir,
		"SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 10",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"row": rowID}}}},
	)
	if len(history.Results[0].Rows) < 2 {
		t.Fatalf("an updated Row should carry its earlier revisions, got %#v", history.Results[0].Rows)
	}

	if ok, message := run(t, dataDir, "DELETE FROM work.notes WHERE row_id = :row",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"row": rowID}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, ExpectedRevision: 2, MaxAffectedRows: 1,
				Actor: "agent:test", Source: "event_4", Reason: "no longer wanted",
			},
		}},
	); !ok {
		t.Fatalf("DELETE failed: %s", message)
	}

	if ok, _ := run(t, dataDir, "SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 10",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"row": rowID}}}},
	); ok {
		t.Fatal("a deleted Row must take its History with it")
	}

	// There is no way back: no UNARCHIVE ROW, and RESTORE rewinds a live Row
	// rather than resurrecting a deleted one.
	if ok, message := run(t, dataDir, "UNARCHIVE ROW work.notes :row",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"row": rowID}}}},
	); ok {
		t.Fatal("UNARCHIVE ROW must not exist")
	} else if !strings.Contains(message, "DATABASE, TABLE or COLUMN") {
		t.Fatalf("the grammar should say archiving is for containers, got %q", message)
	}
	if ok, message := run(t, dataDir, "RESTORE work.notes ROW :row TO REVISION :revision",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"row": rowID, "revision": 1}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, ExpectedRevision: 3, MaxAffectedRows: 1,
				Actor: "agent:test", Source: "event_5", Reason: "try to resurrect",
			},
		}},
	); ok {
		t.Fatal("RESTORE must not resurrect a deleted Row")
	} else if !strings.Contains(message, "final") {
		t.Fatalf("the error should say deletion is final, got %q", message)
	}
}

// TestAsOfCannotReadADeletedRow closes the third door in the same contract.
// SHOW HISTORY and RESTORE are already refused above; AS OF is addressable by
// exactly the same thing — a RowID someone kept — and it returns the values
// themselves, so leaving it open would hand back in full what the other two
// refuse to describe.
//
// The revision numbers this test names are obtainable in practice: SHOW CHANGES
// keeps reporting a deleted Row's ID with its before/after revisions.
func TestAsOfCannotReadADeletedRow(t *testing.T) {
	dataDir, rowID, _, _, _ := rowArchiveFixture(t)
	parameters := []executor.StatementInput{{
		Parameters: executor.Parameters{Named: map[string]any{"row": rowID, "revision": 1}},
	}}

	// While the Row is alive, AS OF is a supported read. Pinning that here keeps
	// the fix honest: the point is to refuse deleted Rows, not to disable AS OF.
	alive := executeTraceMSQL(t, dataDir,
		"SELECT title, revision FROM work.notes AS OF REVISION :revision WHERE row_id = :row LIMIT 1",
		parameters,
	)
	if len(alive.Results[0].Rows) != 1 {
		t.Fatalf("AS OF should read an earlier revision of a live Row, got %#v", alive.Results[0].Rows)
	}

	if ok, message := run(t, dataDir, "DELETE FROM work.notes WHERE row_id = :row",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"row": rowID}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, ExpectedRevision: 2, MaxAffectedRows: 1,
				Actor: "agent:test", Source: "event_6", Reason: "no longer wanted",
			},
		}},
	); !ok {
		t.Fatalf("DELETE failed: %s", message)
	}

	if ok, _ := run(t,
		dataDir,
		"SELECT title, revision FROM work.notes AS OF REVISION :revision WHERE row_id = :row LIMIT 1",
		parameters,
	); ok {
		t.Fatal("AS OF REVISION must not read back a deleted Row")
	}
	if ok, _ := run(t,
		dataDir,
		"SELECT title FROM work.notes AS OF COMMIT_SEQUENCE :revision WHERE row_id = :row LIMIT 1",
		parameters,
	); ok {
		t.Fatal("AS OF COMMIT_SEQUENCE must not read back a deleted Row")
	}
}

// TestDeletingARelationIsFinal is the same rule one level over: a link is cheap
// to recreate, so UNRELATE has no inverse.
func TestDeletingARelationIsFinal(t *testing.T) {
	dataDir, _, relationID, _, _ := rowArchiveFixture(t)

	if ok, message := run(t, dataDir, "UNRELATE :relation",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"relation": relationID}},
			Mutation: executor.MutationOptions{
				MaxAffectedRows: 1, ExpectedRevision: 1,
				Actor: "agent:test", Source: "event_4", Reason: "wrong direction",
			},
		}},
	); !ok {
		t.Fatalf("UNRELATE failed: %s", message)
	}
	if ok, message := run(t, dataDir, "UNARCHIVE RELATION :relation",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"relation": relationID}}}},
	); ok {
		t.Fatal("UNARCHIVE RELATION must not exist")
	} else if !strings.Contains(message, "DATABASE, TABLE or COLUMN") {
		t.Fatalf("the grammar should say archiving is for containers, got %q", message)
	}
}
