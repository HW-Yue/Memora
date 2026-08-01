package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routetrace"
	"github.com/HW-Yue/Memora/internal/security"
)

func TestRunRecordsReadsReopensAndPrunesRouteTraces(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan State, 1)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, dataDir, ready) }()
	<-ready
	executeTraceMSQL(t, dataDir, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'", nil)
	executeTraceMSQL(t, dataDir,
		"CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(40) NOT NULL PURPOSE 'Title')", nil,
	)
	described := executeTraceMSQL(t, dataDir, "DESCRIBE TABLE work.notes", nil)
	databaseID, _ := described.Results[0].Rows[0]["database_id"].(string)
	tableID, _ := described.Results[0].Rows[0]["table_id"].(string)
	if databaseID == "" || tableID == "" {
		t.Fatalf("DESCRIBE identities = %#v", described.Results[0].Rows)
	}
	now := time.Now().UTC()
	draft := daemonTraceDraft("a", databaseID, tableID, now, now.Add(time.Hour))
	authorization := security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:host",
		AuthorizedDatabases: []string{databaseID},
	}
	wrongAuthorization := authorization
	wrongAuthorization.AuthorizedDatabases = []string{"db_other"}
	if _, err := RecordRouteTrace(context.Background(), dataDir, draft, wrongAuthorization); err == nil {
		t.Fatal("cross-scope Route Trace record succeeded")
	}
	recorded, err := RecordRouteTrace(context.Background(), dataDir, draft, authorization)
	if err != nil || recorded.Sequence != 1 || recorded.Checksum == "" {
		t.Fatalf("RecordRouteTrace() = %#v, %v", recorded, err)
	}
	timeline := executeTraceMSQL(t, dataDir, "SHOW ROUTE TRACES IN DATABASE work LIMIT 10", nil)
	if len(timeline.Results[0].Rows) != 1 || timeline.Results[0].Rows[0]["steps"] != nil {
		t.Fatalf("daemon Route Trace timeline = %#v", timeline.Results[0])
	}
	detail := executeTraceMSQL(t, dataDir,
		"SHOW ROUTE TRACE :trace IN DATABASE work LIMIT 1",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"trace": recorded.TraceID}}}},
	)
	if len(detail.Results[0].Rows) != 1 || detail.Results[0].NextCursor == "" || detail.Results[0].Rows[0]["prompt"] != nil {
		t.Fatalf("daemon Route Trace steps = %#v", detail.Results[0])
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	reopenContext, stopReopened := context.WithCancel(context.Background())
	reopenedReady := make(chan State, 1)
	reopenedDone := make(chan error, 1)
	go func() { reopenedDone <- Run(reopenContext, dataDir, reopenedReady) }()
	<-reopenedReady
	reopened := executeTraceMSQL(t, dataDir,
		"SHOW ROUTE TRACE :trace IN DATABASE work LIMIT 10",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"trace": recorded.TraceID}}}},
	)
	if len(reopened.Results[0].Rows) != 2 || reopened.Results[0].Rows[1]["operation"] != "OPEN" {
		t.Fatalf("reopened Route Trace = %#v", reopened.Results[0])
	}
	expired := daemonTraceDraft("b", databaseID, tableID, now.Add(-2*time.Hour), now.Add(-time.Hour))
	if _, err := RecordRouteTrace(context.Background(), dataDir, expired, authorization); err != nil {
		t.Fatal(err)
	}
	deleted, err := PruneRouteTraces(context.Background(), dataDir, now)
	if err != nil || deleted != 1 {
		t.Fatalf("PruneRouteTraces() = %d, %v", deleted, err)
	}
	remaining := executeTraceMSQL(t, dataDir, "SHOW ROUTE TRACES IN DATABASE work LIMIT 10", nil)
	if len(remaining.Results[0].Rows) != 1 || fmt.Sprint(remaining.Results[0].Rows[0]["trace_sequence"]) != "1" {
		t.Fatalf("remaining Route Traces = %#v", remaining.Results[0].Rows)
	}
	stopReopened()
	if err := <-reopenedDone; err != nil {
		t.Fatal(err)
	}
}

func daemonTraceDraft(suffix, databaseID, tableID string, recordedAt, expiresAt time.Time) routetrace.Draft {
	return routetrace.Draft{
		TraceID:    "trace_" + suffix + strings.Repeat("0", 31),
		RecordedAt: recordedAt, ExpiresAt: expiresAt, Actor: "agent:host",
		DatabaseID: databaseID, TableID: tableID, Status: routetrace.StatusComplete,
		Steps: []routetrace.Step{
			{Ordinal: 1, Operation: routetrace.OperationFrame, ParentRouteID: "route_root",
				CandidateRouteIDs: []string{"route_notes", "route_other"}, SelectedRouteID: "route_notes",
				Outcome: routetrace.OutcomeSucceeded, ElapsedMillis: 3, RemainingBudget: 11},
			{Ordinal: 2, Operation: routetrace.OperationOpen, SelectedRouteID: "route_notes",
				Locators: []routetrace.Locator{{RowID: "row_note", Revision: 1}},
				Outcome:  routetrace.OutcomeSucceeded, ElapsedMillis: 2, RemainingBudget: 9},
		},
	}
}

func executeTraceMSQL(
	t *testing.T, dataDir, source string, statements []executor.StatementInput,
) result.Envelope {
	t.Helper()
	envelope, err := Execute(context.Background(), dataDir, source, statements)
	if err != nil || !envelope.OK || len(envelope.Results) != 1 {
		t.Fatalf("Execute(%q) = %#v, %v", source, envelope, err)
	}
	return envelope
}
