package schemachangeplan_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/schemachangeplan"
)

func TestBuildSchemaChangePlanIsDeterministicAndReviewOnly(t *testing.T) {
	t.Parallel()
	database, table := schemaFixture()
	source := &fakeRows{}
	proposal := schemachangeplan.Proposal{
		Version: schemachangeplan.ProposalVersion, ID: "proposal_schema", Actor: "agent:test",
		SourceEventID: "event:schema", Reason: "clarify title and add optional status",
		ExpectedTableRevision: table.SchemaVersion,
		Changes: []schemachangeplan.ChangeProposal{
			{ID: "status", Action: schemachangeplan.ActionAdd, Definition: &catalog.ColumnDefinition{
				Name: "status", Type: "TEXT(40)", Nullable: true, Purpose: "Workflow status", SemanticRole: "status",
			}},
			{ID: "title", Action: schemachangeplan.ActionRename, ColumnID: "col_title", ExpectedRevision: 2, NewName: "heading"},
		},
	}
	first, err := schemachangeplan.Build(context.Background(), source, database, table, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != schemachangeplan.PlanVersion || first.Status != schemachangeplan.StatusReviewRequired ||
		first.PlanID == "" || first.Hash == "" || first.BaseSchemaHash == "" || len(first.Actions) != 2 ||
		first.Impact.AffectedColumns != 2 || source.calls != 0 {
		t.Fatalf("plan = %#v, row calls = %d", first, source.calls)
	}
	secondProposal := proposal
	secondProposal.Changes = append([]schemachangeplan.ChangeProposal(nil), proposal.Changes...)
	secondProposal.Changes[0], secondProposal.Changes[1] = secondProposal.Changes[1], secondProposal.Changes[0]
	secondTable := table
	secondTable.Columns = append([]catalog.Column(nil), table.Columns...)
	secondTable.Columns[0], secondTable.Columns[1] = secondTable.Columns[1], secondTable.Columns[0]
	secondDatabase := database
	secondDatabase.Tables = []catalog.Table{secondTable}
	second, err := schemachangeplan.Build(context.Background(), source, secondDatabase, secondTable, secondProposal)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("shuffled plan drift = %v\n%#v\n%#v", err, first, second)
	}
}

func TestBuildBlocksIncompatibleConstraintChangesAndRejectsTruncatedScan(t *testing.T) {
	t.Parallel()
	database, table := schemaFixture()
	proposal := schemachangeplan.Proposal{
		Version: schemachangeplan.ProposalVersion, ID: "proposal_narrow", Actor: "agent:test",
		SourceEventID: "event:narrow", Reason: "narrow title", ExpectedTableRevision: table.SchemaVersion,
		Changes: []schemachangeplan.ChangeProposal{{
			ID: "narrow", Action: schemachangeplan.ActionAlter, ColumnID: "col_title", ExpectedRevision: 2,
			Definition: &catalog.ColumnDefinition{Name: "title", Type: "TEXT(3)", Nullable: false, Purpose: "Short title", SemanticRole: "title"},
		}},
	}
	source := &fakeRows{values: []row.Row{{ID: "row_a", Revision: 3, Values: map[string]any{"col_title": "too long"}}}}
	plan, err := schemachangeplan.Build(context.Background(), source, database, table, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != schemachangeplan.StatusBlocked || plan.Impact.IncompatibleRows != 1 ||
		len(plan.Blockers) != 1 || len(plan.RowGuards) != 1 || !plan.Impact.RequiresRowRewrite {
		t.Fatalf("blocked plan = %#v", plan)
	}
	source.more = true
	if _, err := schemachangeplan.Build(context.Background(), source, database, table, proposal); err == nil {
		t.Fatal("truncated compatibility scan unexpectedly produced a plan")
	}
}

func TestBuildBlocksNonNullAddAndRejectsFinalSchemaConflicts(t *testing.T) {
	t.Parallel()
	database, table := schemaFixture()
	proposal := schemachangeplan.Proposal{
		Version: schemachangeplan.ProposalVersion, ID: "proposal_add", Actor: "agent:test",
		SourceEventID: "event:add", Reason: "add required status", ExpectedTableRevision: table.SchemaVersion,
		Changes: []schemachangeplan.ChangeProposal{{
			ID: "status", Action: schemachangeplan.ActionAdd,
			Definition: &catalog.ColumnDefinition{Name: "status", Type: "TEXT(20)", Nullable: false, Purpose: "Status"},
		}},
	}
	plan, err := schemachangeplan.Build(context.Background(), &fakeRows{values: []row.Row{{
		ID: "row_a", Revision: 1, Values: map[string]any{"col_title": "ok"},
	}}}, database, table, proposal)
	if err != nil || plan.Status != schemachangeplan.StatusBlocked || !plan.Impact.RequiresRowRewrite {
		t.Fatalf("ADD NOT NULL plan = %#v, %v", plan, err)
	}
	conflicting := proposal
	conflicting.ID = "proposal_conflict"
	conflicting.Changes = []schemachangeplan.ChangeProposal{{
		ID: "rename", Action: schemachangeplan.ActionRename, ColumnID: "col_title", ExpectedRevision: 2, NewName: "body",
	}}
	if _, err := schemachangeplan.Build(context.Background(), &fakeRows{}, database, table, conflicting); err == nil {
		t.Fatal("conflicting final Column name unexpectedly succeeded")
	}
	conflicting.Changes = []schemachangeplan.ChangeProposal{{
		ID: "duplicate-summary", Action: schemachangeplan.ActionAdd,
		Definition: &catalog.ColumnDefinition{Name: "abstract", Type: "TEXT", Nullable: true, Purpose: "Abstract", SemanticRole: "summary"},
	}}
	if _, err := schemachangeplan.Build(context.Background(), &fakeRows{}, database, table, conflicting); err == nil {
		t.Fatal("duplicate summary role unexpectedly succeeded")
	}
}

func TestBuildRejectsStaleDuplicateOrConflictingChanges(t *testing.T) {
	t.Parallel()
	database, table := schemaFixture()
	base := schemachangeplan.Proposal{
		Version: schemachangeplan.ProposalVersion, ID: "proposal_invalid", Actor: "agent:test",
		SourceEventID: "event:invalid", Reason: "invalid", ExpectedTableRevision: table.SchemaVersion,
		Changes: []schemachangeplan.ChangeProposal{{
			ID: "rename", Action: schemachangeplan.ActionRename, ColumnID: "col_title", ExpectedRevision: 1, NewName: "body",
		}},
	}
	if _, err := schemachangeplan.Build(context.Background(), &fakeRows{}, database, table, base); err == nil {
		t.Fatal("stale/conflicting proposal unexpectedly succeeded")
	}
	base.Changes[0].ExpectedRevision = 2
	base.Changes = append(base.Changes, base.Changes[0])
	base.Changes[1].ID = "rename-again"
	if _, err := schemachangeplan.Build(context.Background(), &fakeRows{}, database, table, base); err == nil {
		t.Fatal("duplicate Column action unexpectedly succeeded")
	}
}

func schemaFixture() (catalog.Database, catalog.Table) {
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	table := catalog.Table{ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Purpose: "Notes",
		RowSemantics: "One note", SchemaVersion: 4, CreatedAt: now, UpdatedAt: now,
		Columns: []catalog.Column{
			{ID: "col_title", Name: "title", Aliases: []string{"name"}, Type: "TEXT", MaxCharacters: 100,
				Nullable: false, Purpose: "Title", SemanticRole: "title", SchemaVersion: 2, CreatedAt: now, UpdatedAt: now},
			{ID: "col_body", Name: "body", Aliases: []string{}, Type: "TEXT", MaxCharacters: 1200,
				Nullable: true, Purpose: "Body", SemanticRole: "summary", SchemaVersion: 1, CreatedAt: now, UpdatedAt: now},
		}}
	database := catalog.Database{ID: "db_work", Name: "work", Purpose: "Work", Scope: "Private",
		SchemaVersion: 5, CreatedAt: now, UpdatedAt: now, Tables: []catalog.Table{table}}
	return database, table
}

type fakeRows struct {
	values []row.Row
	more   bool
	calls  int
}

func (source *fakeRows) ListPage(context.Context, string, string, int) ([]row.Row, bool, error) {
	source.calls++
	return append([]row.Row(nil), source.values...), source.more, nil
}
