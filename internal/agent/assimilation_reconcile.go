package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

const AssimilationPlanApprovalVersion = "memora.assimilation-plan-approval/v1"

var (
	ErrInvalidAssimilationReconciler     = errors.New("Assimilation Reconciler dependencies are invalid")
	ErrInvalidAssimilationReconciliation = errors.New("Assimilation reconciliation input is invalid")
	ErrAssimilationPlanApprovalRequired  = errors.New("Assimilation plan requires exact user approval")
	ErrAssimilationReconciliationFailed  = errors.New("Assimilation reconciliation failed")
)

type AssimilationReconciliationRequest struct {
	SubmissionID  string                       `json:"submission_id"`
	SourceID      string                       `json:"source_id"`
	SourceLocator string                       `json:"source_locator"`
	SourceSHA256  string                       `json:"source_sha256"`
	Coverage      ReadCoverageReceipt          `json:"coverage"`
	Batches       []DraftClaimBatch            `json:"batches"`
	Reviews       []AssimilationReviewArtifact `json:"reviews"`
}

type AssimilationPlanApproval struct {
	Version    string `json:"version"`
	PlanSHA256 string `json:"plan_sha256"`
	ApprovedBy string `json:"approved_by"`
	Confirmed  bool   `json:"confirmed"`
}

type AssimilationReconciler struct {
	mu        sync.Mutex
	runtime   *Runtime
	profile   WriteProfile
	submitted map[string]struct{}
}

func NewAssimilationReconciler(
	executor MSQLExecutor,
	profile WriteProfile,
) (*AssimilationReconciler, error) {
	if executor == nil || !validWriteProfile(profile) {
		return nil, ErrInvalidAssimilationReconciler
	}
	runtime, err := New(Dependencies{MSQL: executor})
	if err != nil {
		return nil, ErrInvalidAssimilationReconciler
	}
	return &AssimilationReconciler{
		runtime: runtime, profile: cloneWriteProfile(profile), submitted: make(map[string]struct{}),
	}, nil
}

func (reconciler *AssimilationReconciler) Prepare(
	ctx context.Context,
	request AssimilationReconciliationRequest,
) (protocolmsql.AssimilationPlan, error) {
	if reconciler == nil || ctx == nil {
		return protocolmsql.AssimilationPlan{}, ErrInvalidAssimilationReconciliation
	}
	if err := ctx.Err(); err != nil {
		return protocolmsql.AssimilationPlan{}, err
	}
	proposal, err := reconciler.proposal(request)
	if err != nil {
		return protocolmsql.AssimilationPlan{}, err
	}
	wantPlan, err := protocolmsql.SealAssimilationPlan(proposal)
	if err != nil {
		return protocolmsql.AssimilationPlan{}, ErrInvalidAssimilationReconciliation
	}
	database := assimilationMSQLDatabase(proposal.Database)
	envelope, err := reconciler.runtime.ExecuteMSQL(ctx, protocolmsql.Request{
		Version: protocolmsql.RequestVersion,
		Source:  "REVIEW ASSIMILATION FOR DATABASE " + database + " USING :proposal",
		Statements: []protocolmsql.StatementInput{{
			Parameters:    protocolmsql.Parameters{Named: map[string]any{"proposal": proposal}},
			Authorization: reconciler.authorization(protocolmsql.LevelRead, nil),
		}},
	})
	if err != nil {
		return protocolmsql.AssimilationPlan{}, err
	}
	value, err := assimilationEnvelopeValue(envelope, "assimilation_plan")
	if err != nil {
		return protocolmsql.AssimilationPlan{}, err
	}
	var plan protocolmsql.AssimilationPlan
	if decodeAssimilationReconciliationValue(value, &plan) != nil || plan.Validate() != nil ||
		plan.SHA256 != wantPlan.SHA256 || !reflect.DeepEqual(plan, wantPlan) {
		return protocolmsql.AssimilationPlan{}, ErrAssimilationReconciliationFailed
	}
	return cloneAssimilationReconciliationPlan(plan), nil
}

func (reconciler *AssimilationReconciler) SubmitApproved(
	ctx context.Context,
	plan protocolmsql.AssimilationPlan,
	approval AssimilationPlanApproval,
) (protocolmsql.AssimilationReceipt, error) {
	if reconciler == nil || ctx == nil || !reconciler.validPlan(plan) {
		return protocolmsql.AssimilationReceipt{}, ErrInvalidAssimilationReconciliation
	}
	if approval.Version != AssimilationPlanApprovalVersion || !approval.Confirmed ||
		approval.PlanSHA256 != plan.SHA256 || !validWriteText(approval.ApprovedBy, 160) {
		return protocolmsql.AssimilationReceipt{}, ErrAssimilationPlanApprovalRequired
	}
	if err := ctx.Err(); err != nil {
		return protocolmsql.AssimilationReceipt{}, err
	}
	reconciler.mu.Lock()
	_, alreadySubmitted := reconciler.submitted[plan.SHA256]
	if !alreadySubmitted {
		reconciler.submitted[plan.SHA256] = struct{}{}
	}
	reconciler.mu.Unlock()
	if alreadySubmitted {
		return reconciler.Reconcile(ctx, plan)
	}

	protocolApproval := &protocolmsql.Approval{
		Version: protocolmsql.ApprovalVersion, Action: protocolmsql.AssimilationSubmitAction,
		SubjectSHA256: strings.TrimPrefix(plan.SHA256, "sha256:"), Confirmed: true,
	}
	database := assimilationMSQLDatabase(plan.Proposal.Database)
	envelope, submitErr := reconciler.runtime.ExecuteMSQL(ctx, protocolmsql.Request{
		Version: protocolmsql.RequestVersion,
		Source:  "SUBMIT ASSIMILATION PLAN :plan FOR DATABASE " + database,
		Statements: []protocolmsql.StatementInput{{
			Parameters:    protocolmsql.Parameters{Named: map[string]any{"plan": cloneAssimilationReconciliationPlan(plan)}},
			Authorization: reconciler.authorization(protocolmsql.LevelWrite, protocolApproval),
		}},
	})
	var submitted *protocolmsql.AssimilationReceipt
	if submitErr == nil {
		if value, valueErr := assimilationEnvelopeValue(envelope, "assimilation_receipt"); valueErr == nil {
			var receipt protocolmsql.AssimilationReceipt
			if decodeAssimilationReconciliationValue(value, &receipt) == nil &&
				reconciler.validReceipt(plan, receipt) == nil {
				submitted = &receipt
			}
		}
	}
	reconciled, reconcileErr := reconciler.Reconcile(ctx, plan)
	if reconcileErr != nil {
		if submitErr != nil {
			return protocolmsql.AssimilationReceipt{}, fmt.Errorf(
				"%w: submit: %v; receipt lookup: %v", ErrAssimilationReconciliationFailed, submitErr, reconcileErr,
			)
		}
		return protocolmsql.AssimilationReceipt{}, reconcileErr
	}
	if submitted != nil && !sameAssimilationReceipt(*submitted, reconciled) {
		return protocolmsql.AssimilationReceipt{}, fmt.Errorf(
			"%w: submit and receipt lookup disagree", ErrAssimilationReconciliationFailed,
		)
	}
	return reconciled, nil
}

func (reconciler *AssimilationReconciler) Reconcile(
	ctx context.Context,
	plan protocolmsql.AssimilationPlan,
) (protocolmsql.AssimilationReceipt, error) {
	if reconciler == nil || ctx == nil || !reconciler.validPlan(plan) {
		return protocolmsql.AssimilationReceipt{}, ErrInvalidAssimilationReconciliation
	}
	database := assimilationMSQLDatabase(plan.Proposal.Database)
	envelope, err := reconciler.runtime.ExecuteMSQL(ctx, protocolmsql.Request{
		Version: protocolmsql.RequestVersion,
		Source:  "SHOW ASSIMILATION RECEIPT :receipt IN DATABASE " + database,
		Statements: []protocolmsql.StatementInput{{
			Parameters:    protocolmsql.Parameters{Named: map[string]any{"receipt": plan.PlanID}},
			Authorization: reconciler.authorization(protocolmsql.LevelRead, nil),
		}},
	})
	if err != nil {
		return protocolmsql.AssimilationReceipt{}, err
	}
	value, err := assimilationEnvelopeValue(envelope, "assimilation_receipt")
	if err != nil {
		return protocolmsql.AssimilationReceipt{}, err
	}
	var receipt protocolmsql.AssimilationReceipt
	if decodeAssimilationReconciliationValue(value, &receipt) != nil {
		return protocolmsql.AssimilationReceipt{}, ErrAssimilationReconciliationFailed
	}
	if err := reconciler.validReceipt(plan, receipt); err != nil {
		return protocolmsql.AssimilationReceipt{}, err
	}
	return cloneAssimilationReconciliationReceipt(receipt), nil
}

func (reconciler *AssimilationReconciler) proposal(
	request AssimilationReconciliationRequest,
) (protocolmsql.AssimilationProposal, error) {
	if !validSourceIntakeID(request.SubmissionID, 200) || !validWriteText(request.SourceID, 200) ||
		!validWriteText(request.SourceLocator, 1000) || !validSourceSHA256(request.SourceSHA256) ||
		request.Coverage.Version != ReadCoverageReceiptVersion || !request.Coverage.Complete ||
		!validSourceSHA256(request.Coverage.DocumentSHA256) || request.Coverage.Revision == 0 ||
		request.Coverage.TotalNodes == 0 || request.Coverage.CoveredNodes != request.Coverage.TotalNodes ||
		len(request.Batches) == 0 {
		return protocolmsql.AssimilationProposal{}, ErrInvalidAssimilationReconciliation
	}
	batches := make([]DraftClaimBatch, len(request.Batches))
	for index, batch := range request.Batches {
		batches[index] = cloneDraftClaimBatch(batch)
	}
	sort.Slice(batches, func(left, right int) bool { return batches[left].ExtentSequence < batches[right].ExtentSequence })
	reviews := make(map[uint64]AssimilationReviewArtifact, len(request.Reviews))
	for _, artifact := range request.Reviews {
		if artifact.Validate() != nil || artifact.Status != AssimilationReviewAccepted {
			return protocolmsql.AssimilationProposal{}, ErrInvalidAssimilationReconciliation
		}
		if _, duplicate := reviews[artifact.ExtentSequence]; duplicate {
			return protocolmsql.AssimilationProposal{}, ErrInvalidAssimilationReconciliation
		}
		reviews[artifact.ExtentSequence] = cloneAssimilationReviewArtifact(artifact)
	}
	var database, jobID, author, documentSHA256 string
	statements := make([]protocolmsql.AssimilationStatement, 0)
	reviewers := make([]string, 0, len(reviews))
	artifactSHA256s := make([]string, 0, len(reviews))
	for index, batch := range batches {
		if batch.Validate() != nil || batch.ExtentSequence != uint64(index+1) ||
			batch.DocumentSHA256 != request.Coverage.DocumentSHA256 {
			return protocolmsql.AssimilationProposal{}, ErrInvalidAssimilationReconciliation
		}
		if index == 0 {
			database, jobID, documentSHA256 = batch.Database, batch.JobID, batch.DocumentSHA256
		}
		if !strings.EqualFold(batch.Database, database) || batch.JobID != jobID ||
			batch.DocumentSHA256 != documentSHA256 {
			return protocolmsql.AssimilationProposal{}, ErrInvalidAssimilationReconciliation
		}
		artifact, hasReview := reviews[batch.ExtentSequence]
		if len(batch.Claims) == 0 {
			if hasReview {
				return protocolmsql.AssimilationProposal{}, ErrInvalidAssimilationReconciliation
			}
			continue
		}
		if !hasReview || artifact.JobID != jobID || !strings.EqualFold(artifact.Database, database) ||
			artifact.DocumentSHA256 != documentSHA256 || artifact.BatchSHA256 != batch.SHA256 ||
			artifact.ExtentSHA256 != batch.ExtentSHA256 || len(artifact.Checks) != len(batch.Claims) {
			return protocolmsql.AssimilationProposal{}, ErrInvalidAssimilationReconciliation
		}
		for claimIndex, claim := range batch.Claims {
			if author == "" {
				author = claim.Candidate.Mutation.Actor
			}
			mutation := claim.Candidate.Mutation
			if mutation.Actor != author || artifact.Author != author ||
				artifact.Checks[claimIndex].ClaimID != claim.ClaimID ||
				artifact.Checks[claimIndex].ClaimSHA256 != claim.SHA256 ||
				mutation.Source != request.SourceID || mutation.SourceLocator != request.SourceLocator ||
				mutation.SourceContentHash != request.SourceSHA256 {
				return protocolmsql.AssimilationProposal{}, ErrInvalidAssimilationReconciliation
			}
			statements = append(statements, cloneAssimilationStatement(claim.Candidate))
		}
		reviewers = append(reviewers, artifact.Reviewer)
		artifactSHA256s = append(artifactSHA256s, artifact.SHA256)
		delete(reviews, batch.ExtentSequence)
	}
	if len(reviews) != 0 || len(statements) == 0 || uint64(len(statements)) > reconciler.profile.MaxStatements ||
		len(statements) > protocolmsql.MaxAssimilationStatements || author != reconciler.profile.Actor ||
		!writeProfileAuthorizesDatabase(reconciler.profile, database) {
		return protocolmsql.AssimilationProposal{}, ErrInvalidAssimilationReconciliation
	}
	proposal := protocolmsql.AssimilationProposal{
		Version: protocolmsql.AssimilationProposalVersion, ProposalID: request.SubmissionID,
		Database: database, Author: author, SourceID: request.SourceID,
		SourceLocator: request.SourceLocator, SourceSHA256: request.SourceSHA256,
		DocumentSHA256: documentSHA256, CoverageRevision: request.Coverage.Revision,
		CoveredNodes: request.Coverage.CoveredNodes, TotalNodes: request.Coverage.TotalNodes,
		Evidence: &protocolmsql.AssimilationEvidence{
			Version: protocolmsql.AssimilationEvidenceVersion, Author: author,
			Reviewers: reviewers, ReviewArtifactSHA256s: artifactSHA256s,
			CoverageRevision: request.Coverage.Revision, CoveredNodes: request.Coverage.CoveredNodes,
			TotalNodes: request.Coverage.TotalNodes,
		},
		Statements: statements,
	}
	if proposal.Validate() != nil {
		return protocolmsql.AssimilationProposal{}, ErrInvalidAssimilationReconciliation
	}
	return proposal, nil
}

func (reconciler *AssimilationReconciler) validPlan(plan protocolmsql.AssimilationPlan) bool {
	return plan.Validate() == nil && plan.Proposal.Evidence != nil &&
		plan.Proposal.Author == reconciler.profile.Actor &&
		writeProfileAuthorizesDatabase(reconciler.profile, plan.Proposal.Database) &&
		uint64(len(plan.Proposal.Statements)) <= reconciler.profile.MaxStatements
}

func (reconciler *AssimilationReconciler) validReceipt(
	plan protocolmsql.AssimilationPlan,
	receipt protocolmsql.AssimilationReceipt,
) error {
	if receipt.Validate() != nil || receipt.PlanID != plan.PlanID || receipt.PlanSHA256 != plan.SHA256 ||
		!strings.EqualFold(receipt.Database, plan.Proposal.Database) ||
		receipt.SourceID != plan.Proposal.SourceID || receipt.SourceSHA256 != plan.Proposal.SourceSHA256 ||
		receipt.DocumentSHA256 != plan.Proposal.DocumentSHA256 ||
		!reflect.DeepEqual(receipt.Evidence, plan.Proposal.Evidence) {
		return ErrAssimilationReconciliationFailed
	}
	if receipt.Status == protocolmsql.AssimilationInDoubt {
		if len(receipt.Statements) != 0 {
			return ErrAssimilationReconciliationFailed
		}
		return nil
	}
	if receipt.Status != protocolmsql.AssimilationCommitted ||
		len(receipt.Statements) != len(plan.Proposal.Statements) {
		return ErrAssimilationReconciliationFailed
	}
	for index, statement := range receipt.Statements {
		if statement.Index != index || statement.Revision == nil || *statement.Revision == 0 ||
			statement.CommitSequence == nil || *statement.CommitSequence == 0 ||
			statement.ObjectIDs == nil || uint64(len(statement.ObjectIDs)) != statement.AffectedRows {
			return ErrAssimilationReconciliationFailed
		}
	}
	return nil
}

func (reconciler *AssimilationReconciler) authorization(
	level protocolmsql.RiskLevel,
	approval *protocolmsql.Approval,
) protocolmsql.Authorization {
	levels := make(map[string]protocolmsql.RiskLevel, len(reconciler.profile.AuthorizedDatabases))
	for _, database := range reconciler.profile.AuthorizedDatabases {
		levels[database] = level
	}
	return protocolmsql.Authorization{
		Version: protocolmsql.AuthorizationVersion, Actor: reconciler.profile.Actor,
		AuthorizedDatabases: append([]string(nil), reconciler.profile.AuthorizedDatabases...),
		DefaultLevel:        level, DatabaseLevels: levels, Approval: cloneProtocolApproval(approval),
	}
}

func assimilationEnvelopeValue(envelope protocolmsql.Envelope, field string) (any, error) {
	if !envelope.OK || len(envelope.Results) != 1 || envelope.Results[0].Status != "succeeded" ||
		len(envelope.Results[0].Rows) != 1 {
		return nil, ErrAssimilationReconciliationFailed
	}
	value, found := envelope.Results[0].Rows[0][field]
	if !found {
		return nil, ErrAssimilationReconciliationFailed
	}
	return value, nil
}

func decodeAssimilationReconciliationValue(value, target any) error {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || len(encoded) > protocolmsql.MaxAssimilationPlanBytes {
		return ErrAssimilationReconciliationFailed
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil || requireJSONEOF(decoder) != nil {
		return ErrAssimilationReconciliationFailed
	}
	return nil
}

func sameAssimilationReceipt(left, right protocolmsql.AssimilationReceipt) bool {
	left.Replayed = false
	right.Replayed = false
	return reflect.DeepEqual(left, right)
}

func cloneAssimilationReconciliationPlan(plan protocolmsql.AssimilationPlan) protocolmsql.AssimilationPlan {
	var cloned protocolmsql.AssimilationPlan
	if decodeAssimilationReconciliationValue(plan, &cloned) != nil {
		return protocolmsql.AssimilationPlan{}
	}
	return cloned
}

func cloneAssimilationReconciliationReceipt(receipt protocolmsql.AssimilationReceipt) protocolmsql.AssimilationReceipt {
	var cloned protocolmsql.AssimilationReceipt
	if decodeAssimilationReconciliationValue(receipt, &cloned) != nil {
		return protocolmsql.AssimilationReceipt{}
	}
	return cloned
}

func cloneAssimilationStatement(statement protocolmsql.AssimilationStatement) protocolmsql.AssimilationStatement {
	return protocolmsql.AssimilationStatement{
		MSQL: statement.MSQL, Parameters: cloneWriteParameters(statement.Parameters),
		Mutation: cloneWriteMutation(statement.Mutation),
	}
}

func cloneProtocolApproval(approval *protocolmsql.Approval) *protocolmsql.Approval {
	if approval == nil {
		return nil
	}
	cloned := *approval
	return &cloned
}

func writeProfileAuthorizesDatabase(profile WriteProfile, database string) bool {
	for _, allowed := range profile.AuthorizedDatabases {
		if strings.EqualFold(allowed, database) {
			return true
		}
	}
	return false
}

func assimilationMSQLDatabase(database string) string {
	for index, character := range database {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character == '_' || index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return quoteMSQLIdentifier(database)
	}
	return database
}
