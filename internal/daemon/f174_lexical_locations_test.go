package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

func TestNativeDaemonLexicalLocationsReturnCurrentAuthorizedPositionsAndReopen(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	runContext, stop := context.WithCancel(context.Background())
	ready, done := make(chan State, 1), make(chan error, 1)
	go func() { done <- Run(runContext, dataDir, ready) }()
	<-ready

	work := executeF174(t, dataDir, "CREATE DATABASE work PURPOSE 'Work knowledge' SCOPE 'Projects'", nil)
	workID := textFieldF174(t, work.Results[0].Rows[0], "database_id")
	executeF174(t, dataDir,
		"CREATE TABLE work.notes PURPOSE 'Recovery notes' ROW SEMANTICS 'One complete note' (title TEXT(100) NOT NULL PURPOSE 'Title')", nil)
	root := executeF174(t, dataDir,
		"CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'navigationtoken routes'",
		[]executor.StatementInput{{Mutation: executor.MutationOptions{MaxAffectedRows: 1}}})
	routeID := textFieldF174(t, root.Results[0].Rows[0], "route_id")
	mutable := executeF174(t, dataDir, "INSERT INTO work.notes (title) VALUES ('oldtoken recovery')",
		[]executor.StatementInput{{Mutation: executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1}}})
	mutableRowID := textFieldF174(t, mutable.Results[0].Rows[0], "row_id")
	durable := executeF174(t, dataDir, "INSERT INTO work.notes (title) VALUES ('durabletoken knowledge')",
		[]executor.StatementInput{{Mutation: executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1}}})
	durableRowID := textFieldF174(t, durable.Results[0].Rows[0], "row_id")

	private := executeF174(t, dataDir, "CREATE DATABASE private PURPOSE 'Private' SCOPE 'Secrets'", nil)
	privateID := textFieldF174(t, private.Results[0].Rows[0], "database_id")
	executeF174(t, dataDir,
		"CREATE TABLE private.secrets PURPOSE 'Secrets' ROW SEMANTICS 'One secret' (title TEXT(100) NOT NULL PURPOSE 'Title')", nil)
	executeF174(t, dataDir, "INSERT INTO private.secrets (title) VALUES ('navigationtoken private')",
		[]executor.StatementInput{{Mutation: executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1}}})

	workAuthorization := security.Authorization{Version: security.AuthorizationVersion, Actor: "agent:test",
		AuthorizedDatabases: []string{"work"}, DefaultLevel: security.LevelRead}
	navigation := lexicalF174(t, dataDir, "navigationtoken", "", 64, workAuthorization)
	if !hasLocationF174(navigation.Results[0].Rows, "route", routeID) {
		t.Fatalf("Route location missing = %#v", navigation.Results[0].Rows)
	}
	for _, location := range navigation.Results[0].Rows {
		if location["database_id"] == privateID || location["database_id"] != workID {
			t.Fatalf("unauthorized lexical location leaked = %#v", location)
		}
	}

	old := lexicalF174(t, dataDir, "oldtoken", "", 64, workAuthorization)
	rowLocation := locationF174(t, old.Results[0].Rows, "row", mutableRowID)
	if fmt.Sprint(rowLocation["revision"]) != "1" || rowLocation["matched_term_count"] == nil ||
		rowLocation["matched_field_ids"] == nil {
		t.Fatalf("Row lexical location = %#v", rowLocation)
	}
	selected := executeF174(t, dataDir,
		"SELECT title, revision FROM work.notes AS OF REVISION 1 WHERE row_id = :row LIMIT 1",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"row": mutableRowID}}, Authorization: workAuthorization}})
	if len(selected.Results[0].Rows) != 1 || selected.Results[0].Rows[0]["title"] != "oldtoken recovery" {
		t.Fatalf("SQL location dereference = %#v", selected.Results[0].Rows)
	}

	executeF174(t, dataDir, "UPDATE work.notes SET title = 'newtoken revised' WHERE row_id = :row",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"row": mutableRowID}},
			Mutation: executor.MutationOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1}}})
	if rows := lexicalF174(t, dataDir, "oldtoken", "", 64, workAuthorization).Results[0].Rows; len(rows) != 0 {
		t.Fatalf("stale Row term remained = %#v", rows)
	}
	updated := lexicalF174(t, dataDir, "newtoken", "", 64, workAuthorization)
	if got := locationF174(t, updated.Results[0].Rows, "row", mutableRowID); fmt.Sprint(got["revision"]) != "2" {
		t.Fatalf("updated Row location = %#v", got)
	}
	executeF174(t, dataDir, "DELETE FROM work.notes WHERE row_id = :row",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"row": mutableRowID}},
			Mutation: executor.MutationOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 2, MaxAffectedRows: 1}}})
	if rows := lexicalF174(t, dataDir, "newtoken", "", 64, workAuthorization).Results[0].Rows; len(rows) != 0 {
		t.Fatalf("deleted Row term remained = %#v", rows)
	}

	first := lexicalF174(t, dataDir, "knowledge", "", 1, workAuthorization)
	if first.Results[0].Page == nil || !first.Results[0].Truncated || first.Results[0].NextCursor == "" {
		t.Fatalf("first lexical page = %#v", first.Results[0])
	}
	continued := lexicalF174(t, dataDir, "knowledge", first.Results[0].NextCursor, 64, workAuthorization)
	if continued.Results[0].Page == nil || continued.Results[0].Page.Snapshot != first.Results[0].Page.Snapshot {
		t.Fatalf("continued lexical page = %#v", continued.Results[0])
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	reopenContext, stopReopened := context.WithCancel(context.Background())
	reopenedReady, reopenedDone := make(chan State, 1), make(chan error, 1)
	go func() { reopenedDone <- Run(reopenContext, dataDir, reopenedReady) }()
	<-reopenedReady
	reopened := lexicalF174(t, dataDir, "durabletoken", "", 64, workAuthorization)
	if !hasLocationF174(reopened.Results[0].Rows, "row", durableRowID) {
		t.Fatalf("reopened Row location missing = %#v", reopened.Results[0].Rows)
	}
	before := reopened.Results[0].Page.Snapshot
	executeF174(t, dataDir, "REBUILD LEXICAL INDEX", nil)
	after := lexicalF174(t, dataDir, "durabletoken", "", 64, workAuthorization)
	if !hasLocationF174(after.Results[0].Rows, "row", durableRowID) || after.Results[0].Page.Snapshot != before {
		t.Fatalf("rebuild lexical parity = before %q after %#v", before, after.Results[0])
	}
	stopReopened()
	if err := <-reopenedDone; err != nil {
		t.Fatal(err)
	}
}

func lexicalF174(
	t *testing.T, dataDir, query, cursor string, limit int64, authorization security.Authorization,
) result.Envelope {
	t.Helper()
	source := "SHOW LEXICAL LOCATIONS FROM ALL TABLES USING :query LIMIT :limit BYTES 65536"
	parameters := map[string]any{"query": query, "limit": limit}
	if cursor != "" {
		source = "SHOW LEXICAL LOCATIONS FROM ALL TABLES USING :query CURSOR :cursor LIMIT :limit BYTES 65536"
		parameters["cursor"] = cursor
	}
	return executeF174(t, dataDir, source, []executor.StatementInput{{
		Parameters: executor.Parameters{Named: parameters}, Authorization: authorization,
	}})
}

func executeF174(t *testing.T, dataDir, source string, statements []executor.StatementInput) result.Envelope {
	t.Helper()
	envelope, err := Execute(context.Background(), dataDir, source, statements)
	if err != nil || !envelope.OK || len(envelope.Results) != 1 {
		t.Fatalf("Execute(%q) = %#v, %v", source, envelope, err)
	}
	return envelope
}

func textFieldF174(t *testing.T, row result.Row, field string) string {
	t.Helper()
	value, ok := row[field].(string)
	if !ok || value == "" {
		t.Fatalf("%s missing from %#v", field, row)
	}
	return value
}

func hasLocationF174(rows []result.Row, kind, objectID string) bool {
	for _, row := range rows {
		if row["kind"] == kind && row["object_id"] == objectID {
			return true
		}
	}
	return false
}

func locationF174(t *testing.T, rows []result.Row, kind, objectID string) result.Row {
	t.Helper()
	for _, row := range rows {
		if row["kind"] == kind && row["object_id"] == objectID {
			return row
		}
	}
	t.Fatalf("location %s/%s missing from %#v", kind, objectID, rows)
	return nil
}
