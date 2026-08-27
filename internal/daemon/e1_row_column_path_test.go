package daemon

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

// TestLexicalLocationsCarryRowAndColumnPaths is E1 stage 4's gate.
//
// Retrieval answers one question: where in the semantic tree the hit is. Route
// and Table hits have carried their path since stage 2; Row and Column hits did
// not, because "which leaves hold this Row" had no answer that did not cost a
// scan. E3 gave the Row its own route_leaf_ids, so the answer is a field read.
//
// A Row hangs under any number of leaves, so its answer is a list — one path per
// leaf, and no path at all rather than a guessed one when it hangs nowhere. A
// Column's place is its Table's root path with the column's name below it.
//
// See docs/query/predictor-path-only-v1.md §2 and §7.
func TestLexicalLocationsCarryRowAndColumnPaths(t *testing.T) {
	dataDir := archiveInstance(t)
	root := executeTraceMSQL(t, dataDir,
		"CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE :purpose",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"purpose": "Notes root"}},
			Mutation:   executor.MutationOptions{MaxAffectedRows: 1},
		}},
	)
	rootID, _ := root.Results[0].Rows[0]["route_id"].(string)
	leaf := executeTraceMSQL(t, dataDir,
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"parent": rootID, "name": "architecture", "kind": "leaf", "purpose": "Architecture notes",
			}},
			Mutation: executor.MutationOptions{MaxAffectedRows: 1},
		}},
	)
	leafID, _ := leaf.Results[0].Rows[0]["route_id"].(string)
	leafPath, _ := leaf.Results[0].Rows[0]["path"].(string)
	if leafID == "" || leafPath == "" {
		t.Fatalf("leaf fixture = %#v", leaf.Results[0].Rows[0])
	}
	inserted := executeTraceMSQL(t, dataDir,
		"INSERT INTO work.notes (title) VALUES (:title)",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"title": "pathtoken decisions"}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:test",
				Source: "e1_stage4", Reason: "path fixture", RouteLeafIDs: []string{leafID},
			},
		}},
	)
	rowID, _ := inserted.Results[0].Rows[0]["row_id"].(string)

	authorization := security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:test",
		AuthorizedDatabases: []string{"work"}, DefaultLevel: security.LevelRead,
	}
	hits := lexicalF174(t, dataDir, "pathtoken", "", 64, authorization)
	location := locationF174(t, hits.Results[0].Rows, "row", rowID)
	// The value crosses the wire, so a []string arrives as []any.
	paths := textList(t, location["paths"])
	if len(paths) != 1 || paths[0] != leafPath {
		t.Fatalf("Row location paths = %#v (leaf path %q), whole location %#v",
			location["paths"], leafPath, location)
	}
	// One field, not two: a Row's place is the list, and a scalar "path" beside
	// it would be a second answer to the same question.
	if _, exists := location["path"]; exists {
		t.Fatalf("Row location carries both path and paths: %#v", location)
	}

	// A Column sits under its Table's root, named by the column. The hit is
	// picked by Table rather than by position: this instance holds more than
	// one Table with a "title" column, and only this one has a Route root.
	tableID, _ := location["table_id"].(string)
	columns := lexicalF174(t, dataDir, "title", "", 64, authorization)
	column := columnLocationInTable(t, columns.Results[0].Rows, tableID)
	rootPath, _ := root.Results[0].Rows[0]["path"].(string)
	if rootPath == "" {
		t.Fatalf("root fixture has no path: %#v", root.Results[0].Rows[0])
	}
	want := strings.TrimSuffix(rootPath, "/") + "/title"
	if path, _ := column["path"].(string); path != want {
		t.Fatalf("Column location path = %q, want %q; whole location %#v", column["path"], want, column)
	}
	// A Table with no Route root has no place in the tree, so its Column has no
	// path — not a composed one built from names, which would be a second
	// spelling of something the Router never said.
	for _, other := range columns.Results[0].Rows {
		if other["kind"] != "column" || other["table_id"] == tableID {
			continue
		}
		if _, exists := other["path"]; exists {
			t.Fatalf("a Column of an unrouted Table carries a path: %#v", other)
		}
	}
}

func columnLocationInTable(t *testing.T, rows []result.Row, tableID string) result.Row {
	t.Helper()
	for _, value := range rows {
		if value["kind"] == "column" && value["table_id"] == tableID {
			return value
		}
	}
	t.Fatalf("no column location in Table %q among %#v", tableID, rows)
	return nil
}

func textList(t *testing.T, value any) []string {
	t.Helper()
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				t.Fatalf("list item %#v is not text", item)
			}
			values = append(values, text)
		}
		return values
	default:
		t.Fatalf("value %#v is not a list of text", value)
		return nil
	}
}
