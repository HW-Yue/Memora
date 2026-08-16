package skillschema_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/skillschema"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
	"github.com/HW-Yue/Memora/internal/testkit"
)

func TestEnsureCreatesNewDomainAndReusesSemanticSynonyms(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, dictionary, closeSession := schemaSession(t, ctx)
	defer closeSession()
	tool := sessionSchemaTool(session)

	created, err := skillschema.New(tool).Run(ctx, ensurePlan())
	if err != nil {
		t.Fatal(err)
	}
	if created.Receipt.Status != skillschema.ReceiptApplied ||
		created.Receipt.Database.Action != skillschema.ObjectCreated ||
		created.Receipt.Table.Action != skillschema.ObjectCreated {
		t.Fatalf("created receipt = %#v", created.Receipt)
	}
	for _, call := range created.Calls {
		for _, input := range call.Request.Statements {
			if input.Authorization.Actor != "agent:test" ||
				len(input.Authorization.AuthorizedDatabases) != 2 {
				t.Fatalf("call lost Schema Plan Authorization: %#v", call)
			}
		}
	}
	database, err := dictionary.DescribeDatabase(ctx, "work")
	if err != nil || database.Purpose != "Project knowledge" || database.Scope != "Reviewed projects" {
		t.Fatalf("created Database = %#v, %v", database, err)
	}
	table, err := dictionary.DescribeTable(ctx, "work", "notes")
	if err != nil || table.Purpose != "Durable decisions" ||
		table.RowSemantics != "One reviewed decision" || len(table.Columns) != 1 {
		t.Fatalf("created Table = %#v, %v", table, err)
	}

	reuse := ensurePlan()
	reuse.ID = "schema-reuse"
	reuse.Ensure.Database.Name = "project_knowledge"
	reuse.Ensure.DatabaseSynonyms = []string{"work", "projects"}
	reuse.Ensure.Table.Name = "decisions"
	reuse.Ensure.TableSynonyms = []string{"notes", "knowledge"}
	reused, err := skillschema.New(tool).Run(ctx, reuse)
	if err != nil {
		t.Fatal(err)
	}
	if reused.Receipt.Database.Action != skillschema.ObjectReused ||
		reused.Receipt.Table.Action != skillschema.ObjectReused ||
		reused.Receipt.Database.ID != database.ID || reused.Receipt.Table.ID != table.ID {
		t.Fatalf("reused receipt = %#v", reused.Receipt)
	}
	if containsPhase(reused.Calls, skillschema.PhaseApply) {
		t.Fatalf("synonym reuse emitted DDL: %#v", reused.Calls)
	}
}

func TestEnsurePreservesSemanticRole(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, dictionary, closeSession := schemaSession(t, ctx)
	defer closeSession()
	tool := sessionSchemaTool(session)

	plan := ensurePlan()
	plan.Ensure.Table.Columns[0].SemanticRole = "title"
	if _, err := skillschema.New(tool).Run(ctx, plan); err != nil {
		t.Fatal(err)
	}
	table, err := dictionary.DescribeTable(ctx, "work", "notes")
	if err != nil || len(table.Columns) != 1 || table.Columns[0].SemanticRole != "title" {
		t.Fatalf("semantic role was dropped by ensure: %#v, %v", table, err)
	}
}

func TestEnsureRejectsMissingPurposeScopeOrRowSemanticsBeforeToolCall(t *testing.T) {
	t.Parallel()

	for _, mutate := range []func(*skillschema.Plan){
		func(plan *skillschema.Plan) { plan.Ensure.Database.Purpose = "" },
		func(plan *skillschema.Plan) { plan.Ensure.Database.Scope = "" },
		func(plan *skillschema.Plan) { plan.Ensure.Table.Purpose = "" },
		func(plan *skillschema.Plan) { plan.Ensure.Table.RowSemantics = "" },
	} {
		plan := ensurePlan()
		mutate(&plan)
		tool := &recordingSchemaTool{}
		if _, err := skillschema.New(tool).Run(context.Background(), plan); err == nil {
			t.Fatal("Run() error = nil, want semantic metadata validation")
		}
		if len(tool.calls) != 0 {
			t.Fatalf("invalid plan made calls: %#v", tool.calls)
		}
	}
}

func TestMigrationPreviewEnforcesImpactAndFailureRollsBackRenames(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, dictionary, closeSession := schemaSession(t, ctx)
	defer closeSession()
	if _, err := skillschema.New(sessionSchemaTool(session)).Run(ctx, ensurePlan()); err != nil {
		t.Fatal(err)
	}
	plan := migrationPlan()
	impact, err := skillschema.Preview(plan)
	if err != nil {
		t.Fatal(err)
	}
	if impact.AffectedObjects != 2 || impact.AffectedRows != 0 || !impact.Reversible ||
		len(impact.Rollback) != 2 || impact.Hash == "" {
		t.Fatalf("impact = %#v", impact)
	}
	tooSmall := plan
	migrationCopy := *plan.Migration
	tooSmall.Migration = &migrationCopy
	tooSmall.Migration.MaxAffectedObjects = 1
	if _, err := skillschema.Preview(tooSmall); err == nil {
		t.Fatal("Preview() error = nil, want impact limit rejection")
	}

	injected := false
	tool := skillschema.ToolFunc(func(ctx context.Context, call skillschema.Call) (result.Envelope, error) {
		if call.Phase == skillschema.PhaseApply && call.ID == "rename-column" {
			injected = true
			return result.FailedRequest(call.Request.RequestID, result.CodeConstraint, "injected migration failure", false), nil
		}
		return session.Execute(ctx, call.Request), nil
	})
	report, err := skillschema.New(tool).Run(ctx, plan)
	if err == nil || !injected {
		t.Fatalf("Run() error = %v, injected = %t", err, injected)
	}
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) || stable.StableCode() != string(result.CodeConstraint) {
		t.Fatalf("migration error = %v", err)
	}
	if report.Receipt.Status != skillschema.ReceiptRolledBack ||
		len(report.Receipt.Rollbacks) != 1 || !report.Receipt.Verified {
		t.Fatalf("rollback receipt = %#v", report.Receipt)
	}
	table, err := dictionary.DescribeTable(ctx, "work", "notes")
	if err != nil || table.Name != "notes" || table.SchemaVersion != 3 {
		t.Fatalf("rolled back Table = %#v, %v", table, err)
	}
	column, err := dictionary.DescribeColumn(ctx, "work", "notes", "title")
	if err != nil || column.Name != "title" || column.SchemaVersion != 1 {
		t.Fatalf("untouched Column = %#v, %v", column, err)
	}
}

func TestMigrationAppliesAndVerifiesRenamedObjects(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, dictionary, closeSession := schemaSession(t, ctx)
	defer closeSession()
	runner := skillschema.New(sessionSchemaTool(session))
	if _, err := runner.Run(ctx, ensurePlan()); err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(ctx, migrationPlan())
	if err != nil {
		t.Fatal(err)
	}
	if report.Receipt.Status != skillschema.ReceiptApplied || !report.Receipt.Verified ||
		len(report.Receipt.Applied) != 2 || len(report.Receipt.Rollbacks) != 0 {
		t.Fatalf("migration receipt = %#v", report.Receipt)
	}
	table, err := dictionary.DescribeTable(ctx, "work", "knowledge")
	if err != nil || table.Name != "knowledge" || table.SchemaVersion != 3 {
		t.Fatalf("renamed Table = %#v, %v", table, err)
	}
	column, err := dictionary.DescribeColumn(ctx, "work", "knowledge", "heading")
	if err != nil || column.Name != "heading" || column.SchemaVersion != 2 {
		t.Fatalf("renamed Column = %#v, %v", column, err)
	}
	wantVerified := map[string]bool{
		"DESCRIBE TABLE `work`.`knowledge` COMPACT":            false,
		"DESCRIBE COLUMN `work`.`knowledge`.`heading` COMPACT": false,
	}
	for _, call := range report.Calls {
		if call.Phase == skillschema.PhaseVerify {
			if _, ok := wantVerified[call.Request.Source]; ok {
				wantVerified[call.Request.Source] = true
			}
		}
	}
	for source, found := range wantVerified {
		if !found {
			t.Errorf("missing current-name verification %q", source)
		}
	}
}

func ensurePlan() skillschema.Plan {
	return skillschema.Plan{
		Version: skillschema.PlanVersion, ID: "schema-create",
		Actor: "agent:test", SourceEventID: "conversation:event-2", Reason: "new project domain",
		AuthorizedDatabases: []string{"work", "project_knowledge"},
		Ensure: &skillschema.EnsurePlan{
			Database: catalog.DatabaseDefinition{
				Name: "work", Purpose: "Project knowledge", Scope: "Reviewed projects",
			},
			Table: catalog.TableDefinition{
				Name: "notes", Purpose: "Durable decisions", Scope: "Confirmed knowledge",
				RowSemantics: "One reviewed decision",
				Columns: []catalog.ColumnDefinition{{
					Name: "title", Type: "TEXT(200)", Purpose: "Decision title",
				}},
			},
		},
	}
}

func migrationPlan() skillschema.Plan {
	return skillschema.Plan{
		Version: skillschema.PlanVersion, ID: "schema-migrate",
		Actor: "agent:test", SourceEventID: "conversation:event-3", Reason: "clarify names",
		AuthorizedDatabases: []string{"work"},
		Migration: &skillschema.MigrationPlan{
			Database: "work", ExpectedDatabaseVersion: 2, MaxAffectedObjects: 2,
			Steps: []skillschema.MigrationStep{
				{ID: "rename-table", Action: skillschema.RenameTable, Table: "notes", NewName: "knowledge", ExpectedVersion: 1},
				{ID: "rename-column", Action: skillschema.RenameColumn, Table: "knowledge", Column: "title", NewName: "heading", ExpectedVersion: 1},
			},
		},
	}
}

func schemaSession(
	t *testing.T,
	ctx context.Context,
) (*executor.BatchSession, *catalog.Service, func()) {
	t.Helper()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "skill-schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	dictionary := catalog.New(database, catalog.Options{
		IDs: testkit.NewFakeIDs("work", "notes", "title"),
	})
	rows := row.New(database, dictionary, row.Options{})
	session := executor.NewBatchSession(ctx, dictionary, rows)
	return session, dictionary, func() {
		_ = session.Close()
		if err := database.Close(); err != nil {
			t.Errorf("close schema database: %v", err)
		}
	}
}

func sessionSchemaTool(session *executor.BatchSession) skillschema.Tool {
	return skillschema.ToolFunc(func(ctx context.Context, call skillschema.Call) (result.Envelope, error) {
		return session.Execute(ctx, call.Request), nil
	})
}

type recordingSchemaTool struct {
	calls []skillschema.Call
}

func (tool *recordingSchemaTool) Invoke(_ context.Context, call skillschema.Call) (result.Envelope, error) {
	tool.calls = append(tool.calls, call)
	panic("invalid schema plan invoked tool")
}

func containsPhase(calls []skillschema.Call, phase skillschema.Phase) bool {
	for _, call := range calls {
		if call.Phase == phase {
			return true
		}
	}
	return false
}
