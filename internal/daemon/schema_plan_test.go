package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/schemachangeplan"
	"github.com/HW-Yue/Memora/internal/security"
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

func TestNativeDaemonAppliesApprovedSchemaPlanAndReopensCommittedCatalog(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	start := func() (context.CancelFunc, chan error) {
		ctx, cancel := context.WithCancel(context.Background())
		ready, done := make(chan State, 1), make(chan error, 1)
		go func() { done <- Run(ctx, dataDir, ready) }()
		<-ready
		return cancel, done
	}
	stop := func(cancel context.CancelFunc, done chan error) {
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	cancel, done := start()

	executeTraceMSQL(t, dataDir, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Private'", nil)
	executeTraceMSQL(t, dataDir,
		"CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(100) NOT NULL PURPOSE 'Title')", nil,
	)
	table := executeTraceMSQL(t, dataDir, "DESCRIBE TABLE work.notes COMPACT", nil).Results[0].Rows[0]
	column := executeTraceMSQL(t, dataDir, "DESCRIBE COLUMN work.notes.title COMPACT", nil).Results[0].Rows[0]
	proposal := schemachangeplan.Proposal{Version: schemachangeplan.ProposalVersion, ID: "proposal_rename",
		Actor: "agent:test", SourceEventID: "event:rename", Reason: "clarify heading",
		ExpectedTableRevision: uint64Value(t, table["schema_version"]), Changes: []schemachangeplan.ChangeProposal{{
			ID: "rename", Action: schemachangeplan.ActionRename, ColumnID: fmt.Sprint(column["column_id"]),
			ExpectedRevision: uint64Value(t, column["schema_version"]), NewName: "heading",
		}}}
	planned := executeTraceMSQL(t, dataDir, "PLAN SCHEMA CHANGE FOR TABLE work.notes USING :proposal",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"proposal": proposal}}}})
	encoded, err := json.Marshal(planned.Results[0].Rows[0]["schema_change_plan"])
	if err != nil {
		t.Fatal(err)
	}
	var plan schemachangeplan.Plan
	if err := json.Unmarshal(encoded, &plan); err != nil {
		t.Fatal(err)
	}
	applied := executeTraceMSQL(t, dataDir, "APPLY SCHEMA CHANGE PLAN :plan FOR TABLE work.notes",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"plan": plan}},
			Authorization: security.Authorization{Version: security.AuthorizationVersion, Actor: "user:test",
				AuthorizedDatabases: []string{"work"}, Approval: &security.Approval{Version: security.ApprovalVersion,
					Action: security.ActionApplySchemaChange, SubjectSHA256: strings.TrimPrefix(plan.Hash, "sha256:"), Confirmed: true}}}})
	if applied.Results[0].AffectedRows != 1 || applied.Results[0].Rows[0]["verified"] != true ||
		applied.Results[0].Rows[0]["table_revision"] != float64(2) {
		t.Fatalf("native Schema apply = %#v", applied.Results[0])
	}
	updated := executeTraceMSQL(t, dataDir, "DESCRIBE COLUMN work.notes.heading COMPACT", nil).Results[0].Rows[0]
	if updated["name"] != "heading" {
		t.Fatalf("updated Column = %#v", updated)
	}
	stop(cancel, done)
	cancel, done = start()
	defer stop(cancel, done)
	reopened := executeTraceMSQL(t, dataDir, "DESCRIBE COLUMN work.notes.heading COMPACT", nil).Results[0].Rows[0]
	if reopened["name"] != "heading" || fmt.Sprint(reopened["schema_version"]) != "2" {
		t.Fatalf("reopened Column = %#v", reopened)
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
