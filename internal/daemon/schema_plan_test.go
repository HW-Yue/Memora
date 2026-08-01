package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/schemachangeplan"
)

func TestNativeDaemonPlansBlockedSchemaConstraintWithoutWriting(t *testing.T) {
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
	executeTraceMSQL(t, dataDir, "INSERT INTO work.notes (title) VALUES (:title)", []executor.StatementInput{{
		Parameters: executor.Parameters{Named: map[string]any{"title": "too long"}},
		Mutation: executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
			Actor: "agent:test", Source: "event:fixture", Reason: "fixture"},
	}})
	table := executeTraceMSQL(t, dataDir, "DESCRIBE TABLE work.notes COMPACT", nil).Results[0].Rows[0]
	column := executeTraceMSQL(t, dataDir, "DESCRIBE COLUMN work.notes.title COMPACT", nil).Results[0].Rows[0]
	proposal := schemachangeplan.Proposal{
		Version: schemachangeplan.ProposalVersion, ID: "proposal_native", Actor: "agent:test",
		SourceEventID: "event:schema", Reason: "narrow title", ExpectedTableRevision: uint64Value(t, table["schema_version"]),
		Changes: []schemachangeplan.ChangeProposal{{
			ID: "narrow", Action: schemachangeplan.ActionAlter, ColumnID: fmt.Sprint(column["column_id"]),
			ExpectedRevision: uint64Value(t, column["schema_version"]),
			Definition:       &catalog.ColumnDefinition{Name: "title", Type: "TEXT(3)", Purpose: "Short title"},
		}},
	}
	planned := executeTraceMSQL(t, dataDir, "PLAN SCHEMA CHANGE FOR TABLE work.notes USING :proposal", []executor.StatementInput{{
		Parameters: executor.Parameters{Named: map[string]any{"proposal": proposal}},
	}})
	if planned.Results[0].AffectedRows != 0 || planned.Results[0].Rows[0]["status"] != "blocked" ||
		planned.Results[0].Rows[0]["plan_hash"] == "" {
		t.Fatalf("native Schema plan = %#v", planned.Results[0])
	}
	after := executeTraceMSQL(t, dataDir, "DESCRIBE COLUMN work.notes.title COMPACT", nil).Results[0].Rows[0]
	if fmt.Sprint(after["max_characters"]) != fmt.Sprint(column["max_characters"]) ||
		fmt.Sprint(after["schema_version"]) != fmt.Sprint(column["schema_version"]) {
		t.Fatalf("Schema planning mutated Column: before=%#v after=%#v", column, after)
	}
}

func uint64Value(t *testing.T, value any) uint64 {
	t.Helper()
	var parsed uint64
	if _, err := fmt.Sscan(fmt.Sprint(value), &parsed); err != nil || parsed == 0 {
		t.Fatalf("invalid revision value %#v", value)
	}
	return parsed
}
