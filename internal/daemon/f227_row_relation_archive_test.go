package daemon

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
)

// rowArchiveFixture builds two Routed Rows and one Relation between them, so
// archiving either end has something real to hide.
func rowArchiveFixture(t *testing.T) (string, string, string, string) {
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
	return dataDir, sourceRow, relationID, first
}

// TestArchivingARowHidesItAndUnarchiveRestoresTheSameRow pins that the inverse
// lands on the same Row: re-inserting would mint a new RowID and orphan every
// reference to the old one.
func TestArchivingARowHidesItAndUnarchiveRestoresTheSameRow(t *testing.T) {
	dataDir, rowID, _, leaf := rowArchiveFixture(t)

	before := rowCount(t, dataDir, "SELECT title FROM work.notes LIMIT 10")

	if ok, message := run(t, dataDir, "ARCHIVE ROW work.notes :row REASON :reason",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"row": rowID, "reason": "superseded"}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
				Actor: "agent:test", Source: "event_4", Reason: "superseded",
			},
		}},
	); !ok {
		t.Fatalf("ARCHIVE ROW failed: %s", message)
	}
	if after := rowCount(t, dataDir, "SELECT title FROM work.notes LIMIT 10"); after != before-1 {
		t.Fatalf("archived Row must leave SELECT, got %d of %d", after, before)
	}

	// A restored Row must be reachable again: F224 forbids a live Row that no
	// Route points at, and archiving cleared the memberships.
	if ok, message := run(t, dataDir, "UNARCHIVE ROW work.notes :row",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"row": rowID}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
				Actor: "agent:test", Source: "event_5", Reason: "restore",
			},
		}},
	); ok {
		t.Fatalf("UNARCHIVE ROW without a Route snapshot must fail: %s", message)
	}

	if ok, message := run(t, dataDir, "UNARCHIVE ROW work.notes :row",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"row": rowID}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1, RouteLeafIDs: []string{leaf},
				Actor: "agent:test", Source: "event_6", Reason: "restore",
			},
		}},
	); !ok {
		t.Fatalf("UNARCHIVE ROW failed: %s", message)
	}
	restored := executeTraceMSQL(t, dataDir,
		"SELECT title, row_id FROM work.notes WHERE row_id = :row LIMIT 1",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"row": rowID}}}},
	)
	if len(restored.Results[0].Rows) != 1 || restored.Results[0].Rows[0]["row_id"] != rowID {
		t.Fatalf("UNARCHIVE must restore the same RowID, got %#v", restored.Results[0].Rows)
	}
}

// TestArchivingARelationKeepsItsIdentity is the same point one level over:
// re-running RELATE would produce a different relation_id.
func TestArchivingARelationKeepsItsIdentity(t *testing.T) {
	dataDir, _, relationID, _ := rowArchiveFixture(t)

	if ok, message := run(t, dataDir, "ARCHIVE RELATION :relation REASON :reason",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"relation": relationID, "reason": "wrong direction"}},
			Mutation: executor.MutationOptions{
				MaxAffectedRows: 1, ExpectedRevision: 1,
				Actor: "agent:test", Source: "event_4", Reason: "wrong direction",
			},
		}},
	); !ok {
		t.Fatalf("ARCHIVE RELATION failed: %s", message)
	}

	restore := []executor.StatementInput{{
		Parameters: executor.Parameters{Named: map[string]any{"relation": relationID}},
		Mutation: executor.MutationOptions{
			MaxAffectedRows: 1, ExpectedRevision: 2,
			Actor: "agent:test", Source: "event_5", Reason: "restore",
		},
	}}
	envelope, err := Execute(t.Context(), dataDir, "UNARCHIVE RELATION :relation", restore)
	if err != nil || !envelope.OK {
		message := ""
		for _, statement := range envelope.Results {
			if statement.Error != nil {
				message = statement.Error.Message
			}
		}
		t.Fatalf("UNARCHIVE RELATION failed: %s (%v)", message, err)
	}
	if envelope.Results[0].Rows[0]["id"] != relationID {
		t.Fatalf("UNARCHIVE must restore the same relation_id, got %#v", envelope.Results[0].Rows[0])
	}
	if ok, _ := run(t, dataDir, "UNARCHIVE RELATION :relation", restore); ok {
		t.Fatal("unarchiving a live Relation must fail")
	}
}

func TestArchiveRowAndRelationRequireReason(t *testing.T) {
	dataDir, rowID, relationID, _ := rowArchiveFixture(t)

	for _, source := range []string{
		"ARCHIVE ROW work.notes :row",
		"ARCHIVE RELATION :relation",
	} {
		ok, message := run(t, dataDir, source, []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"row": rowID, "relation": relationID}},
			Mutation:   executor.MutationOptions{MaxAffectedRows: 1, ExpectedRevision: 1},
		}})
		if ok {
			t.Fatalf("%q without REASON must fail", source)
		}
		if !strings.Contains(strings.ToUpper(message), "REASON") {
			t.Fatalf("%q error should name REASON, got %q", source, message)
		}
	}
}
