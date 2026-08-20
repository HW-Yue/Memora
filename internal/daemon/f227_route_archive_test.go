package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
)

// TestNativeDaemonArchivesAndRestoresARouteLeaf pins F227 Stage 1: archiving a
// Route node hides it from navigation without destroying its memberships, and
// UNARCHIVE brings the exact same leaf back with the Row still attached.
func TestNativeDaemonArchivesAndRestoresARouteLeaf(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan State, 1)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, dataDir, ready) }()
	<-ready
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	executeTraceMSQL(t, dataDir, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Private'", nil)
	executeTraceMSQL(t, dataDir,
		"CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(100) NOT NULL PURPOSE 'Title')", nil,
	)
	rootResult := executeTraceMSQL(t, dataDir,
		"CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE :purpose",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"purpose": "Notes root"}},
			Mutation:   executor.MutationOptions{MaxAffectedRows: 1},
		}},
	)
	rootID, _ := rootResult.Results[0].Rows[0]["route_id"].(string)
	leafResult := executeTraceMSQL(t, dataDir,
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"parent": rootID, "name": "decisions", "kind": "leaf", "purpose": "Decision Row",
			}},
			Mutation: executor.MutationOptions{MaxAffectedRows: 1},
		}},
	)
	leafID, _ := leafResult.Results[0].Rows[0]["route_id"].(string)
	executeTraceMSQL(t, dataDir,
		"INSERT INTO work.notes (title) VALUES (:title)",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"title": "first"}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:test", Source: "event_1",
				Reason: "archive fixture", RouteLeafIDs: []string{leafID},
			},
		}},
	)

	openRoute := func() (int, bool) {
		t.Helper()
		envelope, err := Execute(context.Background(), dataDir, "OPEN ROUTE :leaf LIMIT 1",
			[]executor.StatementInput{{
				Parameters: executor.Parameters{Named: map[string]any{"leaf": leafID}},
			}},
		)
		if err != nil {
			t.Fatal(err)
		}
		if !envelope.OK {
			return 0, false
		}
		return len(envelope.Results[0].Rows), true
	}
	if rows, ok := openRoute(); !ok || rows != 1 {
		t.Fatalf("fixture leaf should hold one Row, got %d rows ok=%v", rows, ok)
	}

	archive := executeTraceMSQL(t, dataDir,
		"ARCHIVE ROUTE :leaf REASON :reason",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"leaf": leafID, "reason": "superseded by a wider decision leaf",
			}},
			Mutation: executor.MutationOptions{MaxAffectedRows: 1, ExpectedRevision: 1},
		}},
	)
	if archive.Results[0].Revision == nil || *archive.Results[0].Revision != 2 {
		t.Fatalf("ARCHIVE ROUTE revision = %#v", archive.Results[0].Revision)
	}
	if _, ok := openRoute(); ok {
		t.Fatal("an archived leaf must not be openable")
	}
	children, err := Execute(context.Background(), dataDir, "SHOW ROUTES UNDER :parent LIMIT 12",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"parent": rootID}}}},
	)
	if err != nil || !children.OK {
		t.Fatalf("SHOW ROUTES = %#v, %v", children, err)
	}
	if len(children.Results[0].Rows) != 0 {
		t.Fatalf("an archived leaf must leave the child listing, got %#v", children.Results[0].Rows)
	}

	restore := executeTraceMSQL(t, dataDir,
		"UNARCHIVE ROUTE :leaf",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"leaf": leafID}},
			Mutation:   executor.MutationOptions{MaxAffectedRows: 1, ExpectedRevision: 2},
		}},
	)
	if restore.Results[0].Revision == nil || *restore.Results[0].Revision != 3 {
		t.Fatalf("UNARCHIVE ROUTE revision = %#v", restore.Results[0].Revision)
	}
	if rows, ok := openRoute(); !ok || rows != 1 {
		t.Fatalf("restore must bring the membership back, got %d rows ok=%v", rows, ok)
	}
}

// TestNativeDaemonRefusesArchiveWithoutReason keeps the audit trail mandatory:
// ARCHIVE always records why, UNARCHIVE does not need one.
func TestNativeDaemonRefusesArchiveWithoutReason(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan State, 1)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, dataDir, ready) }()
	<-ready
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	envelope, err := Execute(context.Background(), dataDir, "ARCHIVE ROUTE :leaf",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"leaf": "route_x"}}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.OK {
		t.Fatal("ARCHIVE without REASON must fail")
	}
	message := ""
	if envelope.Error != nil {
		message = envelope.Error.Message
	}
	for _, statement := range envelope.Results {
		if statement.Error != nil {
			message += " " + statement.Error.Message
		}
	}
	if !strings.Contains(strings.ToUpper(message), "REASON") {
		t.Fatalf("error should name REASON, got %q (%#v)", message, envelope)
	}
}
