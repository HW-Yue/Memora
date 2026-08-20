package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
)

// TestRenamingATableKeepsItsRowsReadable pins a data-availability rule that
// the indexed read path used to break: a Catalog change bumps the Table's
// SchemaVersion, but existing Rows keep the SchemaVersion they were written
// at, and the engine deliberately does not rewrite them (schema-change plans
// carry RequiresRowRewrite as a separate, conditional impact). Requiring the
// Row's recorded schema revision to equal the Table's current one therefore
// made every Row unreadable after a rename or any other Catalog bump.
func TestRenamingATableKeepsItsRowsReadable(t *testing.T) {
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
	executeTraceMSQL(t, dataDir,
		"INSERT INTO work.notes (title) VALUES (:title)",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"title": "first"}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:test",
				Source: "event_1", Reason: "rename fixture",
			},
		}},
	)
	executeTraceMSQL(t, dataDir, "ALTER TABLE work.notes RENAME TO memos", nil)

	after := executeTraceMSQL(t, dataDir, "SELECT title FROM work.memos LIMIT 10", nil)
	if len(after.Results[0].Rows) != 1 || after.Results[0].Rows[0]["title"] != "first" {
		t.Fatalf("renaming a Table must not hide its Rows, got %#v", after.Results[0].Rows)
	}
}
