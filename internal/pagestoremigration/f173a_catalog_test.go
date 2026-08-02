package pagestoremigration

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/schemachangeplan"
)

func TestGenerationSeedIncludesCatalogAndRowDocuments(t *testing.T) {
	directory := t.TempDir()
	reader, plan, _ := faultPlan(t)
	applier, err := NewApplier(reader, directory)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := applier.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := OpenGeneration(receipt.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer generation.Close()
	assertCatalogPosting(t, generation, "work", fulltext.KindDatabase, "db_work", 1)
	assertCatalogPosting(t, generation, "notes", fulltext.KindTable, "tbl_notes", 1)
	assertCatalogPosting(t, generation, "title", fulltext.KindColumn, "col_title", 1)
}

func TestAuthorityPublishesCatalogCreateAndRenameRevisions(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	dictionary, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	database, err := dictionary.DescribeDatabase(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}
	assertCatalogPosting(t, authority.Generation(), "work", fulltext.KindDatabase, database.ID, database.SchemaVersion)
	assertCatalogPosting(t, authority.Generation(), "notes", fulltext.KindTable, table.ID, table.SchemaVersion)
	assertCatalogPosting(t, authority.Generation(), "title", fulltext.KindColumn, table.Columns[0].ID, 1)

	renamedDatabase, err := dictionary.RenameDatabase(ctx, "work", "projects")
	if err != nil {
		t.Fatal(err)
	}
	assertCatalogPosting(t, authority.Generation(), "projects", fulltext.KindDatabase, database.ID, renamedDatabase.SchemaVersion)
	assertCatalogPosting(t, authority.Generation(), "work", fulltext.KindDatabase, database.ID, renamedDatabase.SchemaVersion)
	renamedTable, err := dictionary.RenameTable(ctx, "projects", "notes", "memos")
	if err != nil {
		t.Fatal(err)
	}
	assertCatalogPosting(t, authority.Generation(), "memos", fulltext.KindTable, table.ID, renamedTable.SchemaVersion)
	renamedColumn, err := dictionary.RenameColumn(ctx, "projects", "memos", "title", "heading")
	if err != nil {
		t.Fatal(err)
	}
	assertCatalogPosting(t, authority.Generation(), "heading", fulltext.KindColumn, table.Columns[0].ID, renamedColumn.SchemaVersion)
	assertCatalogPosting(t, authority.Generation(), "title", fulltext.KindColumn, table.Columns[0].ID, renamedColumn.SchemaVersion)
}

func TestAuthorityCatalogDropPublishesTombstone(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	dictionary, _, _, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	column, err := dictionary.AddColumn(ctx, "work", "notes", catalog.ColumnDefinition{
		Name: "obsolete", Type: "TEXT(20)", Nullable: true, Purpose: "Vanishingtoken",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCatalogPosting(t, authority.Generation(), "vanishingtoken", fulltext.KindColumn, column.ID, column.SchemaVersion)
	database, err := dictionary.DescribeDatabase(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}
	table, err := dictionary.DescribeTable(ctx, "work", "notes")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := schemachangeplan.Build(ctx, emptySchemaRows{}, database, table, schemachangeplan.Proposal{
		Version: schemachangeplan.ProposalVersion, ID: "proposal_drop_obsolete", Actor: "agent:test",
		SourceEventID: "event:drop", Reason: "remove obsolete field", ExpectedTableRevision: table.SchemaVersion,
		Changes: []schemachangeplan.ChangeProposal{{ID: "drop", Action: schemachangeplan.ActionDrop,
			ColumnID: column.ID, ExpectedRevision: column.SchemaVersion}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := dictionary.ApplySchemaChangePlan(ctx, "work", "notes", plan, nil); err != nil {
		t.Fatal(err)
	}
	assertNoRowPosting(t, authority, "vanishingtoken")
	objects, err := authority.Generation().Fulltext().Objects()
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if object.Kind == fulltext.KindColumn && object.ObjectID == column.ID {
			if object.State != fulltext.StateDeleted || object.Revision != column.SchemaVersion+1 {
				t.Fatalf("dropped Column object = %#v", object)
			}
			return
		}
	}
	t.Fatal("dropped Column tombstone is missing")
}

type emptySchemaRows struct{}

func (emptySchemaRows) ListPage(context.Context, string, string, int) ([]row.Row, bool, error) {
	return []row.Row{}, false, nil
}

func assertCatalogPosting(
	t *testing.T, generation *Generation, term string, kind fulltext.ObjectKind, objectID string, revision uint64,
) {
	t.Helper()
	postings, err := generation.Fulltext().Postings(term)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, posting := range postings {
		if posting.Kind == kind && posting.ObjectID == objectID {
			if posting.Revision != revision {
				t.Fatalf("Postings(%q) stale revision = %#v", term, posting)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("Postings(%q) = %#v", term, postings)
	}
}
