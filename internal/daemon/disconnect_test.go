package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
)

// A client that goes away in the middle of an explicit transaction must cost that
// transaction and nothing else: the Row it had written never lands, and the write
// lock it was holding comes back. The path is one failed frame read —
// internal/ipc/server.go cancels the connection, waits for the in-flight
// requests, runs SessionClosed, and the session's own Close rolls the
// transaction back. Before this test the daemon package had no evidence for any
// of it: a disconnect is the one client behaviour no other test produces.
//
// The lock is the part worth reading. An uncommitted Row is invisible to every
// other session whether or not the transaction was rolled back, so "the Row is
// not there" proves nothing on its own; a second writer getting through proves
// the session ended.
func TestADisconnectRollsBackTheOpenTransaction(t *testing.T) {
	dataDir := t.TempDir()
	stop := serveTestInstance(t, dataDir)
	defer stop()

	holder := dialTestDaemon(t, dataDir)
	for _, source := range []string{
		"CREATE DATABASE work PURPOSE 'Work memory' SCOPE 'Projects'",
		"CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One fact' " +
			"(title TEXT NOT NULL PURPOSE 'Title' ROLE title)",
	} {
		if envelope := executeOnTestDaemon(t, holder, source); !envelope.OK {
			t.Fatalf("%q failed: %s", source, statementErrors(envelope))
		}
	}
	if envelope := executeOnTestDaemon(t, holder, "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Everything'", mutation()); !envelope.OK {
		t.Fatalf("CREATE ROUTE ROOT failed: %s", statementErrors(envelope))
	}
	if envelope := executeOnTestDaemon(t, holder, "BEGIN"); !envelope.OK {
		t.Fatalf("BEGIN failed: %s", statementErrors(envelope))
	}
	if envelope := executeOnTestDaemon(t, holder,
		"INSERT INTO work.notes (title) VALUES ('abandoned with its transaction')", mountedInsert()); !envelope.OK {
		t.Fatalf("INSERT failed: %s", statementErrors(envelope))
	}
	// The write really happened inside the transaction. Without this the absence
	// below would also be explained by an INSERT that never ran.
	if envelope := executeOnTestDaemon(t, holder, "SELECT title FROM work.notes LIMIT 10"); !envelope.OK || rowsIn(envelope) != 1 {
		t.Fatalf("the transaction must see its own write: %s", statementErrors(envelope))
	}

	// The client goes away without COMMIT or ROLLBACK.
	if err := holder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// A second writer gets through, which is only possible if the abandoned
	// session ended and released the write lock. The short budget is the point: a
	// leaked transaction would otherwise sit here until the write-lock timeout.
	other := dialTestDaemon(t, dataDir)
	later := mountedInsert()
	later.Mutation.RoutePath = []router.PathSegment{
		{Name: "afterwards", Kind: router.KindLeaf, Purpose: "Written after the disconnect"},
	}
	if envelope, err := executeWithin(t, other, 5*time.Second,
		"INSERT INTO work.notes (title) VALUES ('written after the disconnect')", later); err != nil {
		t.Fatalf("the abandoned transaction still holds the write lock: %v", err)
	} else if !envelope.OK {
		t.Fatalf("the write after the disconnect failed: %s", statementErrors(envelope))
	}

	// And the abandoned Row never landed: the only Row is the one written after.
	envelope := executeOnTestDaemon(t, other, "SELECT title FROM work.notes LIMIT 10")
	if !envelope.OK {
		t.Fatalf("SELECT failed: %s", statementErrors(envelope))
	}
	if rowsIn(envelope) != 1 || titleOf(envelope) != "written after the disconnect" {
		t.Fatalf("the abandoned transaction's Row is visible: %+v", envelope.Results)
	}
}

// executeWithin is executeOnTestDaemon with its own budget, so a leak can be
// reported as a leak instead of as whichever timeout happens to fire first.
func executeWithin(
	t *testing.T, client *ipc.Client, budget time.Duration, source string, statements ...executor.StatementInput,
) (result.Envelope, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	var envelope result.Envelope
	payload := executePayload{Source: source, Statements: statements}
	if err := client.Call(ctx, "msql.execute", payload, &envelope); err != nil {
		return result.Envelope{}, err
	}
	return envelope, nil
}

// rowsIn counts the Rows of a single-statement answer.
func rowsIn(envelope result.Envelope) int {
	if len(envelope.Results) == 0 {
		return 0
	}
	return len(envelope.Results[0].Rows)
}

func titleOf(envelope result.Envelope) string {
	if len(envelope.Results) == 0 || len(envelope.Results[0].Rows) == 0 {
		return ""
	}
	title, _ := envelope.Results[0].Rows[0]["title"].(string)
	return title
}
