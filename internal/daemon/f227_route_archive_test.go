package daemon

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
)

// A Route node is a semantic index entry: it holds no content of its own and
// is cheap to rebuild, so deleting one is final rather than archived. What the
// engine must guarantee instead is that deleting an index entry can never lose
// a Row — the node has to be empty first.

// TestDeletingARouteLeafRequiresItToBeEmpty pins the safety rule that replaces
// reversibility: you cannot delete a leaf that still points at a Row.
func TestDeletingARouteLeafRequiresItToBeEmpty(t *testing.T) {
	dataDir, rowID, _, leaf, _ := rowArchiveFixture(t)

	ok, message := run(t, dataDir, "DELETE ROUTE :leaf",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"leaf": leaf}},
			Mutation:   executor.MutationOptions{MaxAffectedRows: 1, ExpectedRevision: 1},
		}},
	)
	if ok {
		t.Fatal("deleting a leaf that still holds a Row must fail")
	}
	if !strings.Contains(message, "Row") {
		t.Fatalf("the error must say the leaf still holds a Row, got %q", message)
	}

	// Move the Row into a fresh leaf, and the same delete now succeeds.
	target := createLeaf(t, dataDir, leafParent(t, dataDir, leaf), "spare")
	if ok, message := run(t, dataDir, "UPDATE work.notes SET title = :title WHERE row_id = :row",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"title": "moved", "row": rowID}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, ExpectedRevision: 2, MaxAffectedRows: 1,
				RouteLeafIDs: []string{target},
				Actor:        "agent:test", Source: "event_9", Reason: "move to the other leaf",
			},
		}},
	); !ok {
		t.Fatalf("moving the Row failed: %s", message)
	}
	if ok, message := run(t, dataDir, "DELETE ROUTE :leaf",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"leaf": leaf}},
			Mutation:   executor.MutationOptions{MaxAffectedRows: 1, ExpectedRevision: 1},
		}},
	); !ok {
		t.Fatalf("deleting an emptied leaf must succeed: %s", message)
	}
}

func createLeaf(t *testing.T, dataDir, parentID, name string) string {
	t.Helper()
	created := executeTraceMSQL(t, dataDir,
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"parent": parentID, "name": name, "kind": "leaf", "purpose": name + " Row",
			}},
			Mutation: executor.MutationOptions{MaxAffectedRows: 1},
		}},
	)
	id, _ := created.Results[0].Rows[0]["route_id"].(string)
	return id
}

func leafParent(t *testing.T, dataDir, leafID string) string {
	t.Helper()
	described := executeTraceMSQL(t, dataDir, "DESCRIBE ROUTE :route",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"route": leafID}}}},
	)
	parent, _ := described.Results[0].Rows[0]["parent_id"].(string)
	return parent
}

// TestDeletingARouteIsFinal pins that a deleted node has no way back and no
// archive listing: an index entry is rebuilt by creating a new one.
func TestDeletingARouteIsFinal(t *testing.T) {
	dataDir, _, _, _, rootID := rowArchiveFixture(t)
	empty := createLeaf(t, dataDir, rootID, "spare")

	if ok, message := run(t, dataDir, "DELETE ROUTE :leaf",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"leaf": empty}},
			Mutation:   executor.MutationOptions{MaxAffectedRows: 1, ExpectedRevision: 1},
		}},
	); !ok {
		t.Fatalf("DELETE ROUTE failed: %s", message)
	}

	// No UNARCHIVE, and no archive listing: the grammar itself refuses.
	for _, source := range []string{
		"UNARCHIVE ROUTE :leaf",
		"ARCHIVE ROUTE :leaf REASON :reason",
	} {
		ok, message := run(t, dataDir, source, []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"leaf": empty, "reason": "x"}},
		}})
		if ok {
			t.Fatalf("%q must not be accepted", source)
		}
		if !strings.Contains(message, "DATABASE, TABLE or COLUMN") {
			t.Fatalf("%q should say archiving is for containers, got %q", source, message)
		}
	}
	remaining := executeTraceMSQL(t, dataDir, "SHOW ROUTES UNDER :parent LIMIT 12",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"parent": rootID}}}},
	)
	for _, value := range remaining.Results[0].Rows {
		if id, _ := value["route_id"].(string); id == empty {
			t.Fatalf("the deleted leaf must be gone for good, got %#v", remaining.Results[0].Rows)
		}
	}
}

// TestDeletingARouteWithLiveChildrenSaysWhy keeps the diagnostic honest:
// deletion is bottom-up, and reporting that as a revision conflict sends the
// caller to re-read a revision that was never wrong.
func TestDeletingARouteWithLiveChildrenSaysWhy(t *testing.T) {
	dataDir, _, _, _, rootID := rowArchiveFixture(t)

	ok, message := run(t, dataDir, "DELETE ROUTE :node",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"node": rootID}},
			Mutation:   executor.MutationOptions{MaxAffectedRows: 1, ExpectedRevision: 1},
		}},
	)
	if ok {
		t.Fatal("deleting a node with live children must fail")
	}
	if !strings.Contains(message, "child") {
		t.Fatalf("the error must name the real reason, got %q", message)
	}
}
