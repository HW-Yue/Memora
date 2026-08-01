package executor_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/schemachangeplan"
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
