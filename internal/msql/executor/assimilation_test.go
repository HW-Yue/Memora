package executor_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestAssimilationMSQLReviewsOnlySameDatabaseGuardedL1Mutations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	engine, _, _, closeFixture := assimilationEngineFixture(t, ctx, &fakeAssimilationCommitter{})
	defer closeFixture()
	readContext := assimilationAuthorization(ctx, security.LevelRead, nil)
	proposal := executorAssimilationProposal("INSERT INTO work.notes (title) VALUES (:title)")
	output, err := execute(readContext, engine,
		"REVIEW ASSIMILATION FOR DATABASE work USING :proposal",
		executor.Parameters{Named: map[string]any{"proposal": proposal}}, executor.MutationOptions{},
	)
	if err != nil || len(output.Rows) != 1 || output.Rows[0]["status"] != protocolmsql.AssimilationPlanReview {
		t.Fatalf("review output = %#v, %v", output, err)
	}
	plan, ok := output.Rows[0]["assimilation_plan"].(protocolmsql.AssimilationPlan)
	if !ok || plan.Validate() != nil || plan.Proposal.Statements[0].Parameters.Named["title"] != "Semantic module" {
		t.Fatalf("reviewed plan = %#v", output.Rows[0]["assimilation_plan"])
	}

	for _, source := range []string{
		"ALTER TABLE work.notes ADD COLUMN body TEXT(100) PURPOSE 'Body'",
		"INSERT INTO private.notes (title) VALUES (:title)",
		"BEGIN",
		"REVIEW ASSIMILATION FOR DATABASE work USING :proposal",
	} {
		candidate := executorAssimilationProposal(source)
		if _, err := execute(readContext, engine,
			"REVIEW ASSIMILATION FOR DATABASE work USING :proposal",
			executor.Parameters{Named: map[string]any{"proposal": candidate}}, executor.MutationOptions{},
		); code(err) != string(result.CodeValidation) && code(err) != string(result.CodePermissionDenied) {
			t.Fatalf("review nested %q error = %v", source, err)
		}
	}
	unguarded := executorAssimilationProposal("UPDATE work.notes SET title = :title WHERE row_id = :row")
	unguarded.Statements[0].Parameters.Named["row"] = "row-1"
	if _, err := execute(readContext, engine,
		"REVIEW ASSIMILATION FOR DATABASE work USING :proposal",
		executor.Parameters{Named: map[string]any{"proposal": unguarded}}, executor.MutationOptions{},
	); code(err) != string(result.CodeValidation) {
		t.Fatalf("unguarded revision mutation error = %v", err)
	}

	wrongScope := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:author",
		AuthorizedDatabases: []string{"private"}, DefaultLevel: security.LevelRead,
	})
	if _, err := execute(wrongScope, engine,
		"REVIEW ASSIMILATION FOR DATABASE work USING :proposal",
		executor.Parameters{Named: map[string]any{"proposal": proposal}}, executor.MutationOptions{},
	); code(err) != string(result.CodePermissionDenied) {
		t.Fatalf("wrong scope review error = %v", err)
	}
}

func TestAssimilationMSQLSubmitRequiresExactApprovalAndDelegatesOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	committer := &fakeAssimilationCommitter{}
	engine, _, _, closeFixture := assimilationEngineFixture(t, ctx, committer)
	defer closeFixture()
	proposal := executorAssimilationProposal("INSERT INTO work.notes (title) VALUES (:title)")
	plan, err := protocolmsql.SealAssimilationPlan(proposal)
	if err != nil {
		t.Fatal(err)
	}
	writeContext := assimilationAuthorization(ctx, security.LevelWrite, nil)
	parameters := executor.Parameters{Named: map[string]any{"plan": plan}}
	if _, err := execute(writeContext, engine,
		"SUBMIT ASSIMILATION PLAN :plan FOR DATABASE work", parameters, executor.MutationOptions{},
	); code(err) != string(result.CodePermissionDenied) || committer.submitCalls() != 0 {
		t.Fatalf("missing approval error/calls = %v/%d", err, committer.submitCalls())
	}
	wrongApproval := &security.Approval{
		Version: security.ApprovalVersion, Action: protocolmsql.AssimilationSubmitAction,
		SubjectSHA256: strings.Repeat("0", 64), Confirmed: true,
	}
	if _, err := execute(assimilationAuthorization(ctx, security.LevelWrite, wrongApproval), engine,
		"SUBMIT ASSIMILATION PLAN :plan FOR DATABASE work", parameters, executor.MutationOptions{},
	); code(err) != string(result.CodePermissionDenied) || committer.submitCalls() != 0 {
		t.Fatalf("wrong approval error/calls = %v/%d", err, committer.submitCalls())
	}
	approval := &security.Approval{
		Version: security.ApprovalVersion, Action: protocolmsql.AssimilationSubmitAction,
		SubjectSHA256: strings.TrimPrefix(plan.SHA256, "sha256:"), Confirmed: true,
	}
	output, err := execute(assimilationAuthorization(ctx, security.LevelWrite, approval), engine,
		"SUBMIT ASSIMILATION PLAN :plan FOR DATABASE work", parameters, executor.MutationOptions{},
	)
	if err != nil || output.AffectedRows != 1 || committer.submitCalls() != 1 ||
		output.Rows[0]["status"] != protocolmsql.AssimilationCommitted {
		t.Fatalf("submit output/calls = %#v/%d, %v", output, committer.submitCalls(), err)
	}
	loaded, err := execute(assimilationAuthorization(ctx, security.LevelRead, nil), engine,
		"SHOW ASSIMILATION RECEIPT :receipt IN DATABASE work",
		executor.Parameters{Named: map[string]any{"receipt": plan.PlanID}}, executor.MutationOptions{},
	)
	if err != nil || loaded.AffectedRows != 0 || loaded.Rows[0]["receipt_id"] != plan.PlanID {
		t.Fatalf("receipt output = %#v, %v", loaded, err)
	}
}

func TestAssimilationSubmitIsRejectedInsideExplicitTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	committer := &fakeAssimilationCommitter{}
	engine, dictionary, rows, closeFixture := assimilationEngineFixture(t, ctx, committer)
	_ = engine
	defer closeFixture()
	plan, err := protocolmsql.SealAssimilationPlan(
		executorAssimilationProposal("INSERT INTO work.notes (title) VALUES (:title)"),
	)
	if err != nil {
		t.Fatal(err)
	}
	approval := &security.Approval{
		Version: security.ApprovalVersion, Action: protocolmsql.AssimilationSubmitAction,
		SubjectSHA256: strings.TrimPrefix(plan.SHA256, "sha256:"), Confirmed: true,
	}
	authorization, _ := security.AuthorizationFrom(assimilationAuthorization(ctx, security.LevelWrite, approval))
	session := executor.NewBatchSessionWithCapabilities(ctx, dictionary, rows, nil, nil, nil, nil, committer)
	defer session.Close()
	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "explicit-assimilation", Source: "BEGIN;SUBMIT ASSIMILATION PLAN :plan FOR DATABASE work;COMMIT",
		Statements: []executor.StatementInput{
			{Authorization: authorization},
			{Parameters: executor.Parameters{Named: map[string]any{"plan": plan}}, Authorization: authorization},
			{Authorization: authorization},
		},
	})
	if envelope.OK || envelope.Results[1].Error == nil ||
		envelope.Results[1].Error.Code != result.CodeInvalidTransaction || committer.submitCalls() != 0 {
		t.Fatalf("explicit transaction envelope = %#v", envelope)
	}
}

type fakeAssimilationCommitter struct {
	mu    sync.Mutex
	plans []protocolmsql.AssimilationPlan
}

func (fake *fakeAssimilationCommitter) SubmitAssimilation(
	_ context.Context,
	plan protocolmsql.AssimilationPlan,
) (protocolmsql.AssimilationReceipt, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.plans = append(fake.plans, plan)
	return executorAssimilationReceipt(plan), nil
}

func (fake *fakeAssimilationCommitter) AssimilationReceipt(
	_ context.Context,
	database string,
	receiptID string,
) (protocolmsql.AssimilationReceipt, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.plans) == 0 {
		return protocolmsql.AssimilationReceipt{}, &executor.Error{Code: result.CodeNotFound, Message: "receipt not found"}
	}
	plan := fake.plans[len(fake.plans)-1]
	if database != plan.Proposal.Database || receiptID != plan.PlanID {
		return protocolmsql.AssimilationReceipt{}, &executor.Error{Code: result.CodePermissionDenied, Message: "wrong receipt scope"}
	}
	return executorAssimilationReceipt(plan), nil
}

func (fake *fakeAssimilationCommitter) submitCalls() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.plans)
}

func executorAssimilationReceipt(plan protocolmsql.AssimilationPlan) protocolmsql.AssimilationReceipt {
	return protocolmsql.AssimilationReceipt{
		Version: protocolmsql.AssimilationReceiptVersion, ReceiptID: plan.PlanID, PlanID: plan.PlanID,
		PlanSHA256: plan.SHA256, Database: plan.Proposal.Database, SourceID: plan.Proposal.SourceID,
		SourceSHA256: plan.Proposal.SourceSHA256, DocumentSHA256: plan.Proposal.DocumentSHA256,
		Status: protocolmsql.AssimilationCommitted, Statements: []protocolmsql.AssimilationStatementReceipt{{
			Index: 0, Kind: "INSERT", AffectedRows: 1,
		}},
	}
}

func assimilationEngineFixture(
	t *testing.T,
	ctx context.Context,
	committer executor.AssimilationCommitter,
) (*executor.Engine, executor.Catalog, *row.Service, func()) {
	t.Helper()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "assimilation.db"))
	if err != nil {
		t.Fatal(err)
	}
	dictionary := catalog.New(database, catalog.Options{})
	for _, name := range []string{"work", "private"} {
		if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: name, Purpose: name, Scope: name}); err != nil {
			t.Fatal(err)
		}
		if _, err := dictionary.CreateTable(ctx, name, catalog.TableDefinition{
			Name: "notes", Purpose: "Notes", RowSemantics: "One note",
			Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(200)", Purpose: "Title"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows := row.New(database, dictionary, row.Options{})
	return executor.NewWithCapabilities(dictionary, rows, nil, nil, nil, nil, committer), dictionary, rows, func() { _ = database.Close() }
}

func executorAssimilationProposal(source string) protocolmsql.AssimilationProposal {
	return protocolmsql.AssimilationProposal{
		Version: protocolmsql.AssimilationProposalVersion, ProposalID: "assimilation-1",
		Database: "work", Author: "agent:author", SourceID: "source-1",
		SourceLocator: "book.epub", SourceSHA256: executorAssimilationSHA("a"),
		DocumentSHA256: executorAssimilationSHA("b"), CoverageRevision: 2, CoveredNodes: 3, TotalNodes: 3,
		Statements: []protocolmsql.AssimilationStatement{{
			MSQL: source, Parameters: protocolmsql.Parameters{Named: map[string]any{"title": "Semantic module"}},
			Mutation: protocolmsql.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:author",
				Source: "source-1", Reason: "assimilate semantic module",
				SourceKind: protocolmsql.SourceDocumentAnchor, SourceLocator: "book.epub",
				SourceContentHash: executorAssimilationSHA("a"),
			},
		}},
	}
}

func assimilationAuthorization(
	ctx context.Context,
	level security.RiskLevel,
	approval *security.Approval,
) context.Context {
	return security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:author",
		AuthorizedDatabases: []string{"work"}, DefaultLevel: level, Approval: approval,
	})
}

func executorAssimilationSHA(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func TestAssimilationMSQLASTCarriesDedicatedNode(t *testing.T) {
	t.Parallel()
	document, err := parser.Parse("REVIEW ASSIMILATION FOR DATABASE work USING :proposal")
	if err != nil || document.Statement.Assimilation == nil || document.Statement.Show != nil {
		t.Fatalf("AST = %#v, %v", document, err)
	}
}
