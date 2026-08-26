package executor_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/schemachangeplan"
	"github.com/HW-Yue/Memora/internal/security"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestPlanSchemaChangeMSQLReturnsBlockedCompatibilityPlanWithoutWriting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := nativekvstore.Open(filepath.Join(t.TempDir(), "schema-plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dictionary := catalog.New(store, catalog.Options{IDs: &idSource{values: []string{"database", "table", "title"}}})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Private"}); err != nil {
		t.Fatal(err)
	}
	table, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(100)", Purpose: "Title"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := row.New(store, dictionary, row.Options{IDs: &idSource{values: []string{"row"}}})
	if _, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "too long"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
		Metadata:              row.WriteMetadata{Actor: "agent:test", Source: "event:fixture", Reason: "fixture"},
	}); err != nil {
		t.Fatal(err)
	}
	table, err = dictionary.DescribeTable(ctx, "work", "notes")
	if err != nil {
		t.Fatal(err)
	}
	proposal := schemachangeplan.Proposal{
		Version: schemachangeplan.ProposalVersion, ID: "proposal_narrow", Actor: "agent:test",
		SourceEventID: "event:schema", Reason: "narrow title", ExpectedTableRevision: table.SchemaVersion,
		Changes: []schemachangeplan.ChangeProposal{{
			ID: "narrow", Action: schemachangeplan.ActionAlter, ColumnID: table.Columns[0].ID,
			ExpectedRevision: table.Columns[0].SchemaVersion,
			Definition:       &catalog.ColumnDefinition{Name: "title", Type: "TEXT(3)", Purpose: "Short title"},
		}},
	}
	document, err := parser.Parse("PLAN SCHEMA CHANGE FOR TABLE work.notes USING :proposal")
	if err != nil {
		t.Fatal(err)
	}
	output, err := executor.New(dictionary, rows).Execute(ctx, document.Statement,
		executor.Parameters{Named: map[string]any{"proposal": proposal}}, executor.MutationOptions{})
	if err != nil || len(output.Rows) != 1 || output.Rows[0]["status"] != "blocked" ||
		output.Rows[0]["plan_id"] == "" || output.AffectedRows != 0 {
		t.Fatalf("PLAN SCHEMA CHANGE output = %#v, %v", output, err)
	}
	after, err := dictionary.DescribeTable(ctx, "work", "notes")
	if err != nil || after.SchemaVersion != table.SchemaVersion || after.Columns[0].MaxCharacters != 100 {
		t.Fatalf("planning mutated Table = %#v, %v", after, err)
	}
}

func TestApplySchemaChangeMSQLRequiresHashBoundApprovalAndReturnsReceipt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := nativekvstore.Open(filepath.Join(t.TempDir(), "schema-apply.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dictionary := catalog.New(store, catalog.Options{IDs: &idSource{values: []string{"database", "table", "title"}}})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Private"}); err != nil {
		t.Fatal(err)
	}
	table, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{Name: "notes", Purpose: "Notes",
		RowSemantics: "One note", Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT", Purpose: "Title"}}})
	if err != nil {
		t.Fatal(err)
	}
	database, err := dictionary.DescribeDatabase(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}
	base := row.New(store, dictionary, row.Options{})
	rows := &planningRows{Service: base}
	plan, err := schemachangeplan.Build(ctx, rows, database, table, schemachangeplan.Proposal{
		Version: schemachangeplan.ProposalVersion, ID: "proposal_rename", Actor: "agent:test",
		SourceEventID: "event:rename", Reason: "clarify heading", ExpectedTableRevision: table.SchemaVersion,
		Changes: []schemachangeplan.ChangeProposal{{ID: "rename", Action: schemachangeplan.ActionRename,
			ColumnID: table.Columns[0].ID, ExpectedRevision: table.Columns[0].SchemaVersion, NewName: "heading"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := parser.Parse("APPLY SCHEMA CHANGE PLAN :plan FOR TABLE work.notes")
	if err != nil {
		t.Fatal(err)
	}
	engine := executor.New(dictionary, rows)
	parameters := executor.Parameters{Named: map[string]any{"plan": plan}}
	if _, err := engine.Execute(ctx, document.Statement, parameters, executor.MutationOptions{}); err == nil {
		t.Fatal("APPLY without approval unexpectedly succeeded")
	}
	approved := security.WithAuthorization(ctx, security.Authorization{Version: security.AuthorizationVersion,
		Actor: "user:test", AuthorizedDatabases: []string{"work"}, DefaultLevel: security.LevelStructural, Approval: &security.Approval{
			Version: security.ApprovalVersion, Action: security.ActionApplySchemaChange,
			SubjectSHA256: strings.TrimPrefix(plan.Hash, "sha256:"), Confirmed: true,
		}})
	output, err := engine.Execute(approved, document.Statement, parameters, executor.MutationOptions{})
	if err != nil || output.AffectedRows != 1 || !rows.schemaApplied || output.Rows[0]["verified"] != true ||
		output.Rows[0]["table_revision"] != uint64(2) {
		t.Fatalf("APPLY SCHEMA output = %#v, %v", output, err)
	}
	if _, err := executor.New(dictionary, base).Execute(approved, document.Statement, parameters, executor.MutationOptions{}); code(err) != "unsupported_statement" {
		t.Fatalf("legacy APPLY error = %v", err)
	}
}
