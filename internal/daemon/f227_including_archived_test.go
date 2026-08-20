package daemon

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
)

// Without a read surface for archived objects, UNARCHIVE is unusable: the only
// way to name what to restore would be to remember it. INCLUDING ARCHIVED is
// that surface, and it must widen exactly the statement it appears on.
func TestIncludingArchivedIsTheOnlyWayToSeeArchivedObjects(t *testing.T) {
	dataDir := archiveInstance(t)

	archiveWith := func(source, reason string) {
		t.Helper()
		if ok, message := run(t, dataDir, source, []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"reason": reason}},
		}}); !ok {
			t.Fatalf("%s failed: %s", source, message)
		}
	}
	archiveWith("ARCHIVE TABLE work.notes REASON :reason", "merged into work.tasks")

	if count := rowCount(t, dataDir, "SHOW TABLES FROM work"); count != 1 {
		t.Fatalf("the plain listing must hide the archived Table, got %d", count)
	}
	envelope, err := Execute(t.Context(), dataDir, "SHOW TABLES FROM work INCLUDING ARCHIVED", nil)
	if err != nil || !envelope.OK {
		t.Fatalf("SHOW TABLES INCLUDING ARCHIVED = %#v, %v", envelope, err)
	}
	rows := envelope.Results[0].Rows
	if len(rows) != 1 {
		t.Fatalf("INCLUDING ARCHIVED must list exactly the archived Table, got %#v", rows)
	}
	if rows[0]["name"] != "notes" {
		t.Fatalf("wrong Table listed: %#v", rows[0])
	}
	if rows[0]["archived_at"] == nil || rows[0]["archived_reason"] != "merged into work.tasks" {
		t.Fatalf("archived rows must carry when and why: %#v", rows[0])
	}

	// The modifier widens one statement and nothing else: the next plain read
	// is still live-only.
	if count := rowCount(t, dataDir, "SHOW TABLES FROM work"); count != 1 {
		t.Fatal("INCLUDING ARCHIVED must not leak into the following statement")
	}

	detail, err := Execute(t.Context(), dataDir, "DESCRIBE TABLE work.notes INCLUDING ARCHIVED", nil)
	if err != nil || !detail.OK {
		t.Fatalf("DESCRIBE TABLE INCLUDING ARCHIVED = %#v, %v", detail, err)
	}
	if detail.Results[0].Rows[0]["archived_reason"] != "merged into work.tasks" {
		t.Fatalf("DESCRIBE INCLUDING ARCHIVED lost the reason: %#v", detail.Results[0].Rows[0])
	}
}

func TestIncludingArchivedListsArchivedDatabases(t *testing.T) {
	dataDir := archiveInstance(t)

	if ok, message := run(t, dataDir, "ARCHIVE DATABASE work REASON :reason",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"reason": "retired project"}},
		}},
	); !ok {
		t.Fatalf("ARCHIVE DATABASE failed: %s", message)
	}

	if count := rowCount(t, dataDir, "SHOW DATABASES"); count != 0 {
		t.Fatalf("the plain listing must hide the archived Database, got %d", count)
	}
	envelope, err := Execute(t.Context(), dataDir, "SHOW DATABASES INCLUDING ARCHIVED", nil)
	if err != nil || !envelope.OK {
		t.Fatalf("SHOW DATABASES INCLUDING ARCHIVED = %#v, %v", envelope, err)
	}
	rows := envelope.Results[0].Rows
	if len(rows) != 1 || rows[0]["name"] != "work" {
		t.Fatalf("INCLUDING ARCHIVED must list the archived Database, got %#v", rows)
	}
	if rows[0]["archived_reason"] != "retired project" {
		t.Fatalf("archived Database must carry its reason: %#v", rows[0])
	}

	// DESCRIBE ... INCLUDING ARCHIVED reaches the Database that the plain read
	// refuses, which is what makes UNARCHIVE actionable from the UI.
	detail, err := Execute(t.Context(), dataDir, "DESCRIBE DATABASE work INCLUDING ARCHIVED", nil)
	if err != nil || !detail.OK {
		t.Fatalf("DESCRIBE DATABASE INCLUDING ARCHIVED = %#v, %v", detail, err)
	}
	if detail.Results[0].Rows[0]["archived_reason"] != "retired project" {
		t.Fatalf("DESCRIBE INCLUDING ARCHIVED lost the reason: %#v", detail.Results[0].Rows[0])
	}
}

// INCLUDING ARCHIVED is a read widening, never a write one.
func TestIncludingArchivedDoesNotWidenWrites(t *testing.T) {
	dataDir := archiveInstance(t)

	if ok, message := run(t, dataDir, "ARCHIVE TABLE work.notes REASON :reason",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"reason": "merged"}},
		}},
	); !ok {
		t.Fatalf("ARCHIVE TABLE failed: %s", message)
	}
	for _, source := range []string{
		"SELECT title FROM work.notes INCLUDING ARCHIVED LIMIT 10",
		"INSERT INTO work.notes INCLUDING ARCHIVED (title) VALUES ('x')",
	} {
		if ok, _ := run(t, dataDir, source, nil); ok {
			t.Fatalf("%q must not be accepted", source)
		}
	}
}
