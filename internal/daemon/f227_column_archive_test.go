package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
)

func columnFixture(t *testing.T) string {
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
		"CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' ("+
			"title TEXT(100) NOT NULL PURPOSE 'Title', "+
			"draft TEXT(100) PURPOSE 'Scratch note')", nil,
	)
	executeTraceMSQL(t, dataDir,
		"INSERT INTO work.notes (title, draft) VALUES (:title, :draft)",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"title": "first", "draft": "scratch"}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:test",
				Source: "event_1", Reason: "column fixture",
			},
		}},
	)
	return dataDir
}

// TestArchivingAColumnHidesItAndKeepsEveryStoredValue is the point of making
// Column archivable at all: DROP_COLUMN is flagged irreversible, while an
// archived Column keeps its definition and every Row's value, so restoring it
// is exact.
func TestArchivingAColumnHidesItAndKeepsEveryStoredValue(t *testing.T) {
	dataDir := columnFixture(t)

	if ok, message := run(t, dataDir, "ARCHIVE COLUMN work.notes.draft REASON :reason",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"reason": "scratch field is no longer used"}},
		}},
	); !ok {
		t.Fatalf("ARCHIVE COLUMN failed: %s", message)
	}

	if count := rowCount(t, dataDir, "SHOW COLUMNS FROM work.notes"); count != 1 {
		t.Fatalf("an archived Column must leave SHOW COLUMNS, got %d", count)
	}
	// The Rows themselves must stay readable: the archived Column's value is
	// still on disk and must not make the record look corrupt.
	live := executeTraceMSQL(t, dataDir, "SELECT title FROM work.notes LIMIT 10", nil)
	if len(live.Results[0].Rows) != 1 || live.Results[0].Rows[0]["title"] != "first" {
		t.Fatalf("archiving a Column must not hide its Rows, got %#v", live.Results[0].Rows)
	}
	if ok, message := run(t, dataDir, "SELECT draft FROM work.notes LIMIT 10", nil); ok {
		t.Fatal("selecting an archived Column must fail")
	} else if !strings.Contains(message, "draft") {
		t.Fatalf("the error should name the Column, got %q", message)
	}

	archived, err := Execute(t.Context(), dataDir, "SHOW COLUMNS FROM work.notes INCLUDING ARCHIVED", nil)
	if err != nil || !archived.OK {
		t.Fatalf("SHOW COLUMNS INCLUDING ARCHIVED = %#v, %v", archived, err)
	}
	rows := archived.Results[0].Rows
	if len(rows) != 1 || rows[0]["name"] != "draft" {
		t.Fatalf("INCLUDING ARCHIVED must list the archived Column, got %#v", rows)
	}
	if rows[0]["archived_reason"] != "scratch field is no longer used" {
		t.Fatalf("archived Column must carry its reason: %#v", rows[0])
	}

	if ok, message := run(t, dataDir, "UNARCHIVE COLUMN work.notes.draft", nil); !ok {
		t.Fatalf("UNARCHIVE COLUMN failed: %s", message)
	}
	restored := executeTraceMSQL(t, dataDir, "SELECT draft FROM work.notes LIMIT 10", nil)
	if len(restored.Results[0].Rows) != 1 || restored.Results[0].Rows[0]["draft"] != "scratch" {
		t.Fatalf("UNARCHIVE must bring the stored value back, got %#v", restored.Results[0].Rows)
	}
}

// TestArchivedColumnDoesNotBlockWrites checks the other half: a new Row must
// not have to supply a Column nobody can see any more.
func TestArchivedColumnDoesNotBlockWrites(t *testing.T) {
	dataDir := columnFixture(t)

	if ok, message := run(t, dataDir, "ARCHIVE COLUMN work.notes.draft REASON :reason",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"reason": "unused"}},
		}},
	); !ok {
		t.Fatalf("ARCHIVE COLUMN failed: %s", message)
	}
	describe := executeTraceMSQL(t, dataDir, "DESCRIBE TABLE work.notes COMPACT", nil)
	schemaVersion := uint64(0)
	switch value := describe.Results[0].Rows[0]["schema_version"].(type) {
	case json.Number:
		parsed, _ := value.Int64()
		schemaVersion = uint64(parsed)
	case float64:
		schemaVersion = uint64(value)
	case uint64:
		schemaVersion = value
	default:
		t.Fatalf("unexpected schema_version type %T", value)
	}
	if ok, message := run(t, dataDir, "INSERT INTO work.notes (title) VALUES (:title)",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"title": "second"}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: schemaVersion, MaxAffectedRows: 1, Actor: "agent:test",
				Source: "event_2", Reason: "write after archive",
			},
		}},
	); !ok {
		t.Fatalf("writing after a Column archive must work: %s", message)
	}
	if ok, _ := run(t, dataDir, "INSERT INTO work.notes (title, draft) VALUES ('x', 'y')", nil); ok {
		t.Fatal("writing to an archived Column must fail")
	}
	if count := rowCount(t, dataDir, "SELECT title FROM work.notes LIMIT 10"); count != 2 {
		t.Fatalf("both Rows should be readable, got %d", count)
	}
}

func TestArchivingTheLastLiveColumnIsRefused(t *testing.T) {
	dataDir := columnFixture(t)

	if ok, message := run(t, dataDir, "ARCHIVE COLUMN work.notes.draft REASON :reason",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"reason": "unused"}},
		}},
	); !ok {
		t.Fatalf("ARCHIVE COLUMN failed: %s", message)
	}
	// A Table with no visible Column cannot be selected from or written to;
	// archive the Table instead of emptying it one Column at a time.
	if ok, message := run(t, dataDir, "ARCHIVE COLUMN work.notes.title REASON :reason",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"reason": "also unused"}},
		}},
	); ok {
		t.Fatal("archiving the last live Column must fail")
	} else if !strings.Contains(message, "last") {
		t.Fatalf("the error should explain it is the last live Column, got %q", message)
	}
}
