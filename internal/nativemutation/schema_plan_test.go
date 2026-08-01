package nativemutation

import (
	"context"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativechange"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/schemachangeplan"
	"github.com/HW-Yue/Memora/internal/security"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestApprovedSchemaPlanCommitsCatalogChangeAndReceiptAcrossReopen(t *testing.T) {
	path, file, rows, routes, _, _, _ := mutationFixture(t)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{})
	service := NewService(nativerow.NewService(rows, dictionary, nativerow.ServiceOptions{}),
		dictionary, rows, routes, New(file, rows, routes))
	database, err := dictionary.DescribeDatabase(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	table, err := dictionary.DescribeTable(context.Background(), "work", "notes")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := schemachangeplan.Build(context.Background(), service, database, table, schemachangeplan.Proposal{
		Version: schemachangeplan.ProposalVersion, ID: "proposal_rename", Actor: "agent:test",
		SourceEventID: "event:rename", Reason: "clarify heading", ExpectedTableRevision: table.SchemaVersion,
		Changes: []schemachangeplan.ChangeProposal{{ID: "rename", Action: schemachangeplan.ActionRename,
			ColumnID: "col_title", ExpectedRevision: 1, NewName: "heading"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := service.ApplySchemaChangePlan(approvedSchemaPlanContext(plan), "work", "notes", plan)
	if err != nil || !receipt.Verified || receipt.Status != "committed" || receipt.ChangeSequence == 0 ||
		receipt.TableRevision != 2 || receipt.DatabaseRevision != 2 || receipt.CompensationProposal == nil {
		t.Fatalf("ApplySchemaChangePlan() = %#v, %v", receipt, err)
	}
	updated, err := dictionary.DescribeTable(context.Background(), "work", "notes")
	if err != nil || updated.Columns[0].Name != "heading" || updated.Columns[0].SchemaVersion != 2 {
		t.Fatalf("updated Table = %#v, %v", updated, err)
	}
	changes, more, err := nativechange.New(file).ListAfter(0, 10)
	if err != nil || more || len(changes) != 1 || countEntries(changes[0], change.ObjectDatabase) != 1 ||
		countEntries(changes[0], change.ObjectTable) != 1 || countEntries(changes[0], change.ObjectColumn) != 1 {
		t.Fatalf("Schema Change Log = %#v, %v, %v", changes, more, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedTable, err := nativecatalog.NewService(nativecatalog.New(reopened), nativecatalog.ServiceOptions{}).
		DescribeTable(context.Background(), "work", "notes")
	if err != nil || reopenedTable.Columns[0].Name != "heading" || reopenedTable.SchemaVersion != 2 {
		t.Fatalf("reopened Table = %#v, %v", reopenedTable, err)
	}
}

func TestSchemaPlanRequiresApprovalAndRejectsStaleScannedRowsWithoutCatalogWrite(t *testing.T) {
	_, file, rows, routes, _, _, _ := mutationFixture(t)
	t.Cleanup(func() { _ = file.Close() })
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{})
	service := NewService(nativerow.NewService(rows, dictionary, nativerow.ServiceOptions{}),
		dictionary, rows, routes, New(file, rows, routes))
	database, _ := dictionary.DescribeDatabase(context.Background(), "work")
	table, _ := dictionary.DescribeTable(context.Background(), "work", "notes")
	plan, err := schemachangeplan.Build(context.Background(), service, database, table, schemachangeplan.Proposal{
		Version: schemachangeplan.ProposalVersion, ID: "proposal_narrow", Actor: "agent:test",
		SourceEventID: "event:narrow", Reason: "bounded titles", ExpectedTableRevision: table.SchemaVersion,
		Changes: []schemachangeplan.ChangeProposal{{ID: "narrow", Action: schemachangeplan.ActionAlter,
			ColumnID: "col_title", ExpectedRevision: 1, Definition: &catalog.ColumnDefinition{
				Name: "title", Type: "TEXT(10)", Purpose: "Short title",
			}}},
	})
	if err != nil || plan.Status != schemachangeplan.StatusReviewRequired || len(plan.RowGuards) != 2 {
		t.Fatalf("Build() = %#v, %v", plan, err)
	}
	if _, err := service.ApplySchemaChangePlan(context.Background(), "work", "notes", plan); schemaStableCode(err) != result.CodePermissionDenied {
		t.Fatalf("Apply without approval error = %v", err)
	}
	current, err := rows.Read("row_source")
	if err != nil {
		t.Fatal(err)
	}
	current.Revision++
	current.Values["col_title"] = "changed"
	if err := rows.WriteRevision(current); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplySchemaChangePlan(approvedSchemaPlanContext(plan), "work", "notes", plan); schemaStableCode(err) != result.CodeRevisionConflict {
		t.Fatalf("Apply stale plan error = %v", err)
	}
	unchanged, _ := dictionary.DescribeTable(context.Background(), "work", "notes")
	if unchanged.SchemaVersion != 1 || unchanged.Columns[0].MaxCharacters != 20 {
		t.Fatalf("failed apply changed Catalog = %#v", unchanged)
	}
	changes, _, err := nativechange.New(file).ListAfter(0, 10)
	if err != nil || len(changes) != 0 {
		t.Fatalf("failed apply published changes = %#v, %v", changes, err)
	}
}

func approvedSchemaPlanContext(plan schemachangeplan.Plan) context.Context {
	return security.WithAuthorization(context.Background(), security.Authorization{
		Version: security.AuthorizationVersion, Actor: "user:test", AuthorizedDatabases: []string{"work"},
		Approval: &security.Approval{Version: security.ApprovalVersion, Action: security.ActionApplySchemaChange,
			SubjectSHA256: strings.TrimPrefix(plan.Hash, "sha256:"), Confirmed: true},
	})
}

func schemaStableCode(err error) result.Code {
	if stable, ok := err.(interface{ StableCode() string }); ok {
		return result.Code(stable.StableCode())
	}
	return ""
}
