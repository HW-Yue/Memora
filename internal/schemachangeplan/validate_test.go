package schemachangeplan_test

import (
	"context"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/schemachangeplan"
)

func TestValidateAcceptsBuiltPlanAndRejectsTamperingOrBlockedStatus(t *testing.T) {
	t.Parallel()
	database, table := validationFixture()
	plan, err := schemachangeplan.Build(context.Background(), nil, database, table, validationRenameProposal(table))
	if err != nil {
		t.Fatal(err)
	}
	if err := schemachangeplan.Validate(plan); err != nil {
		t.Fatalf("Validate() = %v", err)
	}

	tampered := plan
	tampered.Actions = append([]schemachangeplan.PlannedAction(nil), plan.Actions...)
	after := *plan.Actions[0].After
	tampered.Actions[0].After = &after
	tampered.Actions[0].After.Name = "forged"
	if err := schemachangeplan.Validate(tampered); err == nil {
		t.Fatal("Validate(tampered) unexpectedly succeeded")
	}

	blocked := plan
	blocked.Status = schemachangeplan.StatusBlocked
	if err := schemachangeplan.Validate(blocked); err == nil {
		t.Fatal("Validate(blocked) unexpectedly succeeded")
	}
}

func TestCompensationProposalRequiresASecondPlan(t *testing.T) {
	t.Parallel()
	database, table := validationFixture()
	plan, err := schemachangeplan.Build(context.Background(), nil, database, table, validationRenameProposal(table))
	if err != nil {
		t.Fatal(err)
	}
	proposal, ok := schemachangeplan.CompensationProposal(plan, table.SchemaVersion+1)
	if !ok || proposal.Version != schemachangeplan.ProposalVersion || proposal.ExpectedTableRevision != table.SchemaVersion+1 {
		t.Fatalf("CompensationProposal() = %#v, %v", proposal, ok)
	}
	if len(proposal.Changes) != 1 || proposal.Changes[0].Action != schemachangeplan.ActionRename ||
		proposal.Changes[0].NewName != plan.Actions[0].Before.Name {
		t.Fatalf("compensation changes = %#v", proposal.Changes)
	}

	drop := plan
	drop.Actions[0].Action = schemachangeplan.ActionDrop
	drop.Actions[0].After = nil
	if _, ok := schemachangeplan.CompensationProposal(drop, table.SchemaVersion+1); ok {
		t.Fatal("DROP plan unexpectedly produced compensation")
	}
}

func TestApplyToSnapshotMaterializesAddAlterAndDropAsOneRevision(t *testing.T) {
	t.Parallel()
	database, table := schemaFixture()
	plan, err := schemachangeplan.Build(context.Background(), &fakeRows{}, database, table, schemachangeplan.Proposal{
		Version: schemachangeplan.ProposalVersion, ID: "proposal_mixed", Actor: "agent:test",
		SourceEventID: "event:mixed", Reason: "reshape fields", ExpectedTableRevision: table.SchemaVersion,
		Changes: []schemachangeplan.ChangeProposal{
			{ID: "add_status", Action: schemachangeplan.ActionAdd, Definition: &catalog.ColumnDefinition{
				Name: "status", Type: "TEXT(20)", Nullable: true, Purpose: "Workflow status", SemanticRole: "status"}},
			{ID: "alter_title", Action: schemachangeplan.ActionAlter, ColumnID: "col_title", ExpectedRevision: 2,
				Definition: &catalog.ColumnDefinition{Name: "title", Type: "TEXT(100)", Purpose: "Primary heading", SemanticRole: "title"}},
			{ID: "drop_body", Action: schemachangeplan.ActionDrop, ColumnID: "col_body", ExpectedRevision: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	updatedDatabase, updatedTable, err := schemachangeplan.ApplyToSnapshot(plan, database, table,
		time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	// DROP_COLUMN archives instead of removing: the Column stays in the
	// snapshot carrying its definition, so existing Rows keep decoding and the
	// action is exactly undoable. It is still destructive — the Column leaves
	// every read surface — but no longer irreversible.
	live := catalog.LiveColumns(updatedTable.Columns)
	if updatedDatabase.SchemaVersion != database.SchemaVersion+1 || updatedTable.SchemaVersion != table.SchemaVersion+1 ||
		len(updatedTable.Columns) != 3 || len(live) != 2 ||
		live[0].Purpose != "Primary heading" || live[0].SchemaVersion != 3 ||
		live[1].Name != "status" || live[1].SchemaVersion != 1 ||
		!plan.Impact.Destructive || !plan.Impact.Reversible {
		t.Fatalf("updated snapshot = %#v / %#v", updatedDatabase, updatedTable)
	}
	dropped := updatedTable.Columns[1]
	if dropped.Name != "body" || !dropped.Archived() || dropped.ArchivedReason != "DROP_COLUMN" {
		t.Fatalf("DROP_COLUMN must archive the Column, got %#v", dropped)
	}
	if _, ok := schemachangeplan.CompensationProposal(plan, updatedTable.SchemaVersion); ok {
		t.Fatal("destructive mixed plan unexpectedly produced compensation")
	}
}

func validationFixture() (catalog.Database, catalog.Table) {
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	table := catalog.Table{ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Purpose: "Notes",
		RowSemantics: "One note", SchemaVersion: 4, CreatedAt: now, UpdatedAt: now,
		Columns: []catalog.Column{{ID: "col_title", Name: "title", Aliases: []string{"name"},
			Type: "TEXT", MaxCharacters: 100, Nullable: false, Purpose: "Title", SemanticRole: "title",
			SchemaVersion: 2, CreatedAt: now, UpdatedAt: now}}}
	database := catalog.Database{ID: "db_work", Name: "work", Purpose: "Work", Scope: "Private",
		SchemaVersion: 5, CreatedAt: now, UpdatedAt: now, Tables: []catalog.Table{table}}
	return database, table
}

func validationRenameProposal(table catalog.Table) schemachangeplan.Proposal {
	return schemachangeplan.Proposal{Version: schemachangeplan.ProposalVersion, ID: "proposal_rename",
		Actor: "agent:test", SourceEventID: "event:rename", Reason: "clarify title",
		ExpectedTableRevision: table.SchemaVersion, Changes: []schemachangeplan.ChangeProposal{{
			ID: "rename", Action: schemachangeplan.ActionRename, ColumnID: "col_title",
			ExpectedRevision: 2, NewName: "heading",
		}}}
}
