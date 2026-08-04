package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestAssimilationReconcilerPreparesSubmitsAndReadsSourceReceiptThroughMSQL(t *testing.T) {
	input, plan, receipt := reconciliationFixture(t)
	msql := &shortTextMSQL{responses: []protocolmsql.Envelope{
		reconciliationEnvelope("review", "REVIEW ASSIMILATION", "assimilation_plan", plan),
		reconciliationEnvelope("submit", "SUBMIT ASSIMILATION", "assimilation_receipt", receipt),
		reconciliationEnvelope("show", "SHOW ASSIMILATION RECEIPT", "assimilation_receipt", receipt),
		reconciliationEnvelope("show-replay", "SHOW ASSIMILATION RECEIPT", "assimilation_receipt", receipt),
	}}
	reconciler := newAssimilationReconciler(t, msql)

	prepared, err := reconciler.Prepare(context.Background(), input)
	if err != nil || prepared.Validate() != nil || prepared.SHA256 != plan.SHA256 {
		t.Fatalf("Prepare() = %#v, %v", prepared, err)
	}
	committed, err := reconciler.SubmitApproved(context.Background(), prepared, agent.AssimilationPlanApproval{
		Version: agent.AssimilationPlanApprovalVersion, PlanSHA256: prepared.SHA256,
		ApprovedBy: "user:yy", Confirmed: true,
	})
	if err != nil || committed.Validate() != nil || committed.Status != protocolmsql.AssimilationCommitted ||
		len(committed.Statements) != 2 || committed.Statements[0].ObjectIDs[0] != "row-actual-1" ||
		committed.Statements[1].ObjectIDs[0] != "row-actual-2" || committed.Evidence == nil ||
		committed.Evidence.ReviewArtifactSHA256s[0] != input.Reviews[0].SHA256 {
		t.Fatalf("SubmitApproved() = %#v, %v", committed, err)
	}

	requests := msql.Requests()
	if len(requests) != 3 || requests[0].Source != "REVIEW ASSIMILATION FOR DATABASE work USING :proposal" ||
		requests[1].Source != "SUBMIT ASSIMILATION PLAN :plan FOR DATABASE work" ||
		requests[2].Source != "SHOW ASSIMILATION RECEIPT :receipt IN DATABASE work" {
		t.Fatalf("MSQL requests = %#v", requests)
	}
	proposal, ok := requests[0].Statements[0].Parameters.Named["proposal"].(protocolmsql.AssimilationProposal)
	if !ok || proposal.Validate() != nil || proposal.Evidence == nil ||
		proposal.Evidence.ReviewArtifactSHA256s[0] != input.Reviews[0].SHA256 ||
		len(proposal.Statements) != len(input.Batches[0].Claims) {
		t.Fatalf("proposal = %#v", requests[0].Statements[0].Parameters.Named["proposal"])
	}
	approval := requests[1].Statements[0].Authorization.Approval
	if approval == nil || approval.Action != protocolmsql.AssimilationSubmitAction ||
		approval.SubjectSHA256 != strings.TrimPrefix(plan.SHA256, "sha256:") || !approval.Confirmed {
		t.Fatalf("approval = %#v", approval)
	}
	if requests[0].Statements[0].Authorization.DefaultLevel != protocolmsql.LevelRead ||
		requests[1].Statements[0].Authorization.DefaultLevel != protocolmsql.LevelWrite ||
		requests[2].Statements[0].Authorization.DefaultLevel != protocolmsql.LevelRead {
		t.Fatalf("authorization levels = %#v", requests)
	}

	replayed, err := reconciler.SubmitApproved(context.Background(), prepared, agent.AssimilationPlanApproval{
		Version: agent.AssimilationPlanApprovalVersion, PlanSHA256: prepared.SHA256,
		ApprovedBy: "user:yy", Confirmed: true,
	})
	if err != nil || replayed.PlanSHA256 != committed.PlanSHA256 || msql.Calls() != 4 ||
		msql.Requests()[3].Source != "SHOW ASSIMILATION RECEIPT :receipt IN DATABASE work" {
		t.Fatalf("safe replay = %#v, %v, calls=%d", replayed, err, msql.Calls())
	}
}

func TestAssimilationReconcilerRecoversTransportUncertaintyWithReadOnlyReceipt(t *testing.T) {
	input, plan, receipt := reconciliationFixture(t)
	msql := &shortTextMSQL{
		responses: []protocolmsql.Envelope{
			reconciliationEnvelope("review", "REVIEW ASSIMILATION", "assimilation_plan", plan),
			{},
			reconciliationEnvelope("show", "SHOW ASSIMILATION RECEIPT", "assimilation_receipt", receipt),
		},
		errors: []error{nil, errors.New("transport closed after submit dispatch"), nil},
	}
	reconciler := newAssimilationReconciler(t, msql)
	prepared, err := reconciler.Prepare(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := reconciler.SubmitApproved(context.Background(), prepared, agent.AssimilationPlanApproval{
		Version: agent.AssimilationPlanApprovalVersion, PlanSHA256: prepared.SHA256,
		ApprovedBy: "user:yy", Confirmed: true,
	})
	if err != nil || resolved.Status != protocolmsql.AssimilationCommitted || msql.Calls() != 3 {
		t.Fatalf("resolved uncertainty = %#v, %v, calls=%d", resolved, err, msql.Calls())
	}
	requests := msql.Requests()
	if requests[1].Source != "SUBMIT ASSIMILATION PLAN :plan FOR DATABASE work" ||
		requests[2].Source != "SHOW ASSIMILATION RECEIPT :receipt IN DATABASE work" {
		t.Fatalf("uncertainty requests = %#v", requests)
	}
}

func TestAssimilationReconcilerRejectsIncompleteCoverageOrReviewBeforeMSQL(t *testing.T) {
	input, _, _ := reconciliationFixture(t)
	tests := []struct {
		name   string
		mutate func(*agent.AssimilationReconciliationRequest)
	}{
		{name: "coverage", mutate: func(value *agent.AssimilationReconciliationRequest) {
			value.Coverage.Complete = false
		}},
		{name: "missing review", mutate: func(value *agent.AssimilationReconciliationRequest) {
			value.Reviews = nil
		}},
		{name: "rejected review", mutate: func(value *agent.AssimilationReconciliationRequest) {
			value.Reviews[0].Status = agent.AssimilationReviewRejected
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := input
			invalid.Batches = append([]agent.DraftClaimBatch(nil), input.Batches...)
			invalid.Reviews = append([]agent.AssimilationReviewArtifact(nil), input.Reviews...)
			test.mutate(&invalid)
			msql := &shortTextMSQL{}
			reconciler := newAssimilationReconciler(t, msql)
			if _, err := reconciler.Prepare(context.Background(), invalid); !errors.Is(err, agent.ErrInvalidAssimilationReconciliation) {
				t.Fatalf("Prepare() error = %v", err)
			}
			if msql.Calls() != 0 {
				t.Fatalf("MSQL calls = %d", msql.Calls())
			}
		})
	}
}

func TestAssimilationReconcilerCoalescesConcurrentSubmission(t *testing.T) {
	input, plan, receipt := reconciliationFixture(t)
	msql := &concurrentReconciliationMSQL{plan: plan, receipt: receipt}
	reconciler := newAssimilationReconciler(t, msql)
	prepared, err := reconciler.Prepare(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	approval := agent.AssimilationPlanApproval{
		Version: agent.AssimilationPlanApprovalVersion, PlanSHA256: prepared.SHA256,
		ApprovedBy: "user:yy", Confirmed: true,
	}
	const callers = 16
	var wait sync.WaitGroup
	errorsFound := make(chan error, callers)
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			resolved, submitErr := reconciler.SubmitApproved(context.Background(), prepared, approval)
			if submitErr != nil || resolved.Status != protocolmsql.AssimilationCommitted {
				errorsFound <- errors.Join(submitErr, errors.New("receipt was not committed"))
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for submitErr := range errorsFound {
		t.Fatalf("concurrent SubmitApproved() = %v", submitErr)
	}
	if submits := msql.SubmitCalls(); submits != 1 {
		t.Fatalf("submit calls = %d", submits)
	}
}

func reconciliationFixture(
	t *testing.T,
) (agent.AssimilationReconciliationRequest, protocolmsql.AssimilationPlan, protocolmsql.AssimilationReceipt) {
	t.Helper()
	batch, extent := reviewDraftBatch(t)
	challenge := strings.Repeat("3", 64)
	store, err := agent.OpenAssimilationReviewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reviewer := newAssimilationReviewer(t, &shortTextProvider{response: assimilationReviewResponse(
		t, "review-model", reviewToolArguments(batch, challenge, true),
	)}, store, &reviewChallengeSource{values: []string{challenge}}, "review-model")
	artifact, err := reviewer.Review(context.Background(), agent.AssimilationReviewRequest{
		Batch: batch, Extent: extent, PeerClaims: []agent.DraftClaim{},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := batch.Claims[0].Candidate.Mutation
	input := agent.AssimilationReconciliationRequest{
		SubmissionID: "submission-job-draft-1", SourceID: first.Source,
		SourceLocator: first.SourceLocator, SourceSHA256: first.SourceContentHash,
		Coverage: agent.ReadCoverageReceipt{
			Version: agent.ReadCoverageReceiptVersion, DocumentSHA256: batch.DocumentSHA256,
			Revision: 3, CoveredNodes: extent.EndPosition, TotalNodes: extent.EndPosition, Complete: true,
		},
		Batches: []agent.DraftClaimBatch{batch}, Reviews: []agent.AssimilationReviewArtifact{artifact},
	}
	statements := make([]protocolmsql.AssimilationStatement, len(batch.Claims))
	for index, claim := range batch.Claims {
		statements[index] = claim.Candidate
	}
	proposal := protocolmsql.AssimilationProposal{
		Version: protocolmsql.AssimilationProposalVersion, ProposalID: input.SubmissionID,
		Database: batch.Database, Author: artifact.Author, SourceID: input.SourceID,
		SourceLocator: input.SourceLocator, SourceSHA256: input.SourceSHA256,
		DocumentSHA256: batch.DocumentSHA256, CoverageRevision: input.Coverage.Revision,
		CoveredNodes: input.Coverage.CoveredNodes, TotalNodes: input.Coverage.TotalNodes,
		Evidence: &protocolmsql.AssimilationEvidence{
			Version: protocolmsql.AssimilationEvidenceVersion, Author: artifact.Author,
			Reviewers: []string{artifact.Reviewer}, ReviewArtifactSHA256s: []string{artifact.SHA256},
			CoverageRevision: input.Coverage.Revision, CoveredNodes: input.Coverage.CoveredNodes,
			TotalNodes: input.Coverage.TotalNodes,
		},
		Statements: statements,
	}
	plan, err := protocolmsql.SealAssimilationPlan(proposal)
	if err != nil {
		t.Fatal(err)
	}
	revisionOne, revisionTwo := uint64(1), uint64(1)
	sequenceOne, sequenceTwo := uint64(20), uint64(21)
	receipt := protocolmsql.AssimilationReceipt{
		Version: protocolmsql.AssimilationReceiptVersion, ReceiptID: plan.PlanID, PlanID: plan.PlanID,
		PlanSHA256: plan.SHA256, Database: proposal.Database, SourceID: proposal.SourceID,
		SourceSHA256: proposal.SourceSHA256, DocumentSHA256: proposal.DocumentSHA256,
		Evidence: cloneTestAssimilationEvidence(proposal.Evidence), Status: protocolmsql.AssimilationCommitted,
		Statements: []protocolmsql.AssimilationStatementReceipt{
			{Index: 0, Kind: "INSERT", AffectedRows: 1, ObjectIDs: []string{"row-actual-1"}, Revision: &revisionOne, CommitSequence: &sequenceOne},
			{Index: 1, Kind: "INSERT", AffectedRows: 1, ObjectIDs: []string{"row-actual-2"}, Revision: &revisionTwo, CommitSequence: &sequenceTwo},
		},
	}
	if receipt.Validate() != nil {
		t.Fatalf("fixture receipt = %#v", receipt)
	}
	return input, plan, receipt
}

func newAssimilationReconciler(t *testing.T, msql agent.MSQLExecutor) *agent.AssimilationReconciler {
	t.Helper()
	reconciler, err := agent.NewAssimilationReconciler(msql, agent.WriteProfile{
		Version: agent.WriteProfileVersion, ProfileID: "profile-assimilation",
		Actor: "agent:assimilation-author", AuthorizedDatabases: []string{"work"},
		MaxStatements: 8, MaxAffectedRows: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reconciler
}

func reconciliationEnvelope(requestID, statement, field string, value any) protocolmsql.Envelope {
	return protocolmsql.Envelope{
		Version: protocolmsql.EnvelopeVersion, RequestID: requestID, OK: true,
		Results: []protocolmsql.StatementResult{{
			Index: 0, Statement: statement, Source: statement, Status: "succeeded",
			Columns: []protocolmsql.Column{{Name: field, Type: "JSON"}},
			Rows:    []protocolmsql.Row{{field: value}}, Warnings: []protocolmsql.Notice{},
		}}, Warnings: []protocolmsql.Notice{},
	}
}

func cloneTestAssimilationEvidence(value *protocolmsql.AssimilationEvidence) *protocolmsql.AssimilationEvidence {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Reviewers = append([]string(nil), value.Reviewers...)
	cloned.ReviewArtifactSHA256s = append([]string(nil), value.ReviewArtifactSHA256s...)
	return &cloned
}

type concurrentReconciliationMSQL struct {
	mu      sync.Mutex
	plan    protocolmsql.AssimilationPlan
	receipt protocolmsql.AssimilationReceipt
	submits int
}

func (executor *concurrentReconciliationMSQL) ExecuteMSQL(
	_ context.Context,
	request protocolmsql.Request,
) (protocolmsql.Envelope, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	switch {
	case strings.HasPrefix(request.Source, "REVIEW ASSIMILATION"):
		return reconciliationEnvelope("review", "REVIEW ASSIMILATION", "assimilation_plan", executor.plan), nil
	case strings.HasPrefix(request.Source, "SUBMIT ASSIMILATION"):
		executor.submits++
		return reconciliationEnvelope("submit", "SUBMIT ASSIMILATION", "assimilation_receipt", executor.receipt), nil
	case strings.HasPrefix(request.Source, "SHOW ASSIMILATION"):
		return reconciliationEnvelope("show", "SHOW ASSIMILATION RECEIPT", "assimilation_receipt", executor.receipt), nil
	default:
		return protocolmsql.Envelope{}, errors.New("unexpected MSQL request")
	}
}

func (executor *concurrentReconciliationMSQL) SubmitCalls() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.submits
}
