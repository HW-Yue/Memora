package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
)

func archiveInstance(t *testing.T) string {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "instance")
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan State, 1)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, dataDir, ready) }()
	<-ready
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	executeTraceMSQL(t, dataDir, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Private'", nil)
	executeTraceMSQL(t, dataDir,
		"CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(100) NOT NULL PURPOSE 'Title')", nil,
	)
	executeTraceMSQL(t, dataDir,
		"CREATE TABLE work.tasks PURPOSE 'Tasks' ROW SEMANTICS 'One task' (title TEXT(100) NOT NULL PURPOSE 'Title')", nil,
	)
	executeTraceMSQL(t, dataDir,
		"INSERT INTO work.notes (title) VALUES (:title)",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"title": "first"}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:test",
				Source: "event_1", Reason: "archive fixture",
			},
		}},
	)
	return dataDir
}

func run(t *testing.T, dataDir, source string, statements []executor.StatementInput) (bool, string) {
	t.Helper()
	envelope, err := Execute(context.Background(), dataDir, source, statements)
	if err != nil {
		t.Fatalf("Execute(%q): %v", source, err)
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
	return envelope.OK, message
}

func rowCount(t *testing.T, dataDir, source string) int {
	t.Helper()
	envelope, err := Execute(context.Background(), dataDir, source, nil)
	if err != nil || !envelope.OK {
		t.Fatalf("Execute(%q) = %#v, %v", source, envelope, err)
	}
	return len(envelope.Results[0].Rows)
}

// TestArchivingATableHidesItWithoutTouchingItsRows pins the central invariant:
// visibility comes from the container, so archiving costs zero Row revisions
// and UNARCHIVE brings the Rows back exactly as they were.
func TestArchivingATableHidesItWithoutTouchingItsRows(t *testing.T) {
	dataDir := archiveInstance(t)

	before := executeTraceMSQL(t, dataDir, "SELECT title, row_id, revision FROM work.notes LIMIT 10", nil)
	if len(before.Results[0].Rows) != 1 {
		t.Fatalf("fixture should hold one Row, got %#v", before.Results[0].Rows)
	}
	revision := before.Results[0].Rows[0]["revision"]

	if ok, message := run(t, dataDir, "ARCHIVE TABLE work.notes REASON :reason",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"reason": "merged into work.tasks"}},
		}},
	); !ok {
		t.Fatalf("ARCHIVE TABLE failed: %s", message)
	}

	if count := rowCount(t, dataDir, "SHOW TABLES FROM work"); count != 1 {
		t.Fatalf("an archived Table must leave SHOW TABLES, got %d", count)
	}
	for _, source := range []string{
		"SELECT title FROM work.notes LIMIT 10",
		"DESCRIBE TABLE work.notes",
	} {
		ok, message := run(t, dataDir, source, nil)
		if ok {
			t.Fatalf("%q must fail on an archived Table", source)
		}
		if !strings.Contains(message, "UNARCHIVE") {
			t.Fatalf("%q error should point at UNARCHIVE, got %q", source, message)
		}
	}
	ok, message := run(t, dataDir, "INSERT INTO work.notes (title) VALUES (:title)",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"title": "second"}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:test",
				Source: "event_2", Reason: "should be refused",
			},
		}},
	)
	if ok || !strings.Contains(message, "UNARCHIVE") {
		t.Fatalf("writing to an archived Table must fail naming UNARCHIVE, got ok=%v %q", ok, message)
	}

	if ok, message := run(t, dataDir, "UNARCHIVE TABLE work.notes", nil); !ok {
		t.Fatalf("UNARCHIVE TABLE failed: %s", message)
	}
	after := executeTraceMSQL(t, dataDir, "SELECT title, row_id, revision FROM work.notes LIMIT 10", nil)
	if len(after.Results[0].Rows) != 1 {
		t.Fatalf("UNARCHIVE must bring the Rows back, got %#v", after.Results[0].Rows)
	}
	if after.Results[0].Rows[0]["revision"] != revision {
		t.Fatalf("archiving must not bump a Row revision: %v -> %v", revision, after.Results[0].Rows[0]["revision"])
	}
}

// TestArchivingADatabaseHidesItsTablesAndKeepsTheNamespace covers the ancestor
// rule and the deliberate decision that an archived name is still taken.
func TestArchivingADatabaseHidesItsTablesAndKeepsTheNamespace(t *testing.T) {
	dataDir := archiveInstance(t)

	if ok, message := run(t, dataDir, "ARCHIVE DATABASE work REASON :reason",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"reason": "retired project"}},
		}},
	); !ok {
		t.Fatalf("ARCHIVE DATABASE failed: %s", message)
	}

	if count := rowCount(t, dataDir, "SHOW DATABASES"); count != 0 {
		t.Fatalf("an archived Database must leave SHOW DATABASES, got %d", count)
	}
	for _, source := range []string{
		"SHOW TABLES FROM work",
		"SELECT title FROM work.notes LIMIT 10",
		"DESCRIBE DATABASE work",
	} {
		ok, message := run(t, dataDir, source, nil)
		if ok {
			t.Fatalf("%q must fail under an archived Database", source)
		}
		if !strings.Contains(message, "UNARCHIVE") {
			t.Fatalf("%q error should point at UNARCHIVE, got %q", source, message)
		}
	}
	// The name is still taken: creating over an archived Database must fail
	// loudly rather than silently shadowing it.
	if ok, _ := run(t, dataDir, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Private'", nil); ok {
		t.Fatal("an archived Database must keep occupying its name")
	}

	if ok, message := run(t, dataDir, "UNARCHIVE DATABASE work", nil); !ok {
		t.Fatalf("UNARCHIVE DATABASE failed: %s", message)
	}
	if count := rowCount(t, dataDir, "SHOW TABLES FROM work"); count != 2 {
		t.Fatalf("UNARCHIVE must bring both Tables back, got %d", count)
	}
}

// TestUnarchivingADatabaseKeepsSeparatelyArchivedTablesArchived pins the rule
// that UNARCHIVE reverses exactly one decision and never someone else's.
func TestUnarchivingADatabaseKeepsSeparatelyArchivedTablesArchived(t *testing.T) {
	dataDir := archiveInstance(t)

	if ok, message := run(t, dataDir, "ARCHIVE TABLE work.tasks REASON :reason",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"reason": "unused"}},
		}},
	); !ok {
		t.Fatalf("ARCHIVE TABLE failed: %s", message)
	}
	if ok, message := run(t, dataDir, "ARCHIVE DATABASE work REASON :reason",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"reason": "retired project"}},
		}},
	); !ok {
		t.Fatalf("ARCHIVE DATABASE failed: %s", message)
	}
	if ok, message := run(t, dataDir, "UNARCHIVE DATABASE work", nil); !ok {
		t.Fatalf("UNARCHIVE DATABASE failed: %s", message)
	}

	if count := rowCount(t, dataDir, "SHOW TABLES FROM work"); count != 1 {
		t.Fatalf("work.tasks was archived on its own and must stay archived, got %d Tables", count)
	}
	if ok, _ := run(t, dataDir, "SELECT title FROM work.tasks LIMIT 10", nil); ok {
		t.Fatal("work.tasks must still be archived after the Database is restored")
	}
}

func TestArchiveRequiresReasonAndRefusesDoubleArchive(t *testing.T) {
	dataDir := archiveInstance(t)

	if ok, message := run(t, dataDir, "ARCHIVE TABLE work.notes", nil); ok {
		t.Fatal("ARCHIVE without REASON must fail")
	} else if !strings.Contains(strings.ToUpper(message), "REASON") {
		t.Fatalf("error should name REASON, got %q", message)
	}
	archive := []executor.StatementInput{{
		Parameters: executor.Parameters{Named: map[string]any{"reason": "merged"}},
	}}
	if ok, message := run(t, dataDir, "ARCHIVE TABLE work.notes REASON :reason", archive); !ok {
		t.Fatalf("ARCHIVE TABLE failed: %s", message)
	}
	if ok, _ := run(t, dataDir, "ARCHIVE TABLE work.notes REASON :reason", archive); ok {
		t.Fatal("archiving an already archived Table must fail")
	}
	if ok, _ := run(t, dataDir, "UNARCHIVE TABLE work.tasks", nil); ok {
		t.Fatal("unarchiving a live Table must fail")
	}
}
