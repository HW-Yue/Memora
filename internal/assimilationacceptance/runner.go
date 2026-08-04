package assimilationacceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

const (
	ResultVersion   = "memora.assimilation-chain-acceptance/v1"
	StatusSucceeded = "succeeded"
)

var (
	ErrInvalidRunner = errors.New("assimilation acceptance Runner is invalid")
	ErrRunChain      = errors.New("assimilation acceptance chain failed")
)

type SourceStore interface {
	Put(context.Context, agent.SourcePutRequest, io.Reader) (agent.SourcePutReceipt, error)
	ReleaseJob(string) (agent.SourceCleanupReceipt, error)
}

type EPUBParser interface {
	Parse(context.Context, string, string) (agent.DocumentIR, agent.EPUBParseReceipt, error)
}

type Drafter interface {
	Draft(context.Context, agent.AssimilationDraftRequest) (agent.DraftClaimReceipt, error)
}

type ClaimLedger interface {
	ReadExtent(string, uint64) (agent.DraftClaimBatch, error)
}

type Reviewer interface {
	Review(context.Context, agent.AssimilationReviewRequest) (agent.AssimilationReviewArtifact, error)
}

type Reconciler interface {
	Prepare(context.Context, agent.AssimilationReconciliationRequest) (protocolmsql.AssimilationPlan, error)
	SubmitApproved(context.Context, protocolmsql.AssimilationPlan, agent.AssimilationPlanApproval) (protocolmsql.AssimilationReceipt, error)
}

type QueryAgent interface {
	Query(context.Context, agent.QueryRequest) (agent.QueryResult, error)
}

type PlanApprovalSource interface {
	ApproveAssimilationPlan(context.Context, protocolmsql.AssimilationPlan) (agent.AssimilationPlanApproval, error)
}

type Clock interface {
	Now() time.Time
}

type Dependencies struct {
	Sources    SourceStore
	EPUB       EPUBParser
	Drafter    Drafter
	Ledger     ClaimLedger
	Reviewer   Reviewer
	Reconciler Reconciler
	Query      QueryAgent
	Approvals  PlanApprovalSource
	Clock      Clock
}

type Request struct {
	RunID        string
	JobID        string
	SourceID     string
	FileName     string
	Payload      []byte
	SubmissionID string
	ExtentLimits agent.ReadExtentLimits
	Query        agent.QueryRequest
}

type Stage struct {
	Name           string `json:"name"`
	DurationMicros uint64 `json:"duration_micros"`
}

type Result struct {
	Version             string                           `json:"version"`
	RunID               string                           `json:"run_id"`
	Status              string                           `json:"status"`
	SourceSHA256        string                           `json:"source_sha256"`
	DocumentSHA256      string                           `json:"document_sha256"`
	Parse               agent.EPUBParseReceipt           `json:"parse"`
	Coverage            agent.ReadCoverageReceipt        `json:"coverage"`
	Extents             uint64                           `json:"extents"`
	Claims              uint64                           `json:"claims"`
	Reviews             uint64                           `json:"reviews"`
	PlanID              string                           `json:"plan_id"`
	PlanSHA256          string                           `json:"plan_sha256"`
	SourceReceipt       protocolmsql.AssimilationReceipt `json:"source_receipt"`
	Question            string                           `json:"question"`
	Answer              string                           `json:"answer"`
	SelectEvidenceRows  uint64                           `json:"select_evidence_rows"`
	DraftProviderCalls  uint64                           `json:"draft_provider_calls"`
	ReviewProviderCalls uint64                           `json:"review_provider_calls"`
	QueryProviderCalls  uint64                           `json:"query_provider_calls"`
	DraftUsage          agent.ProviderUsage              `json:"draft_usage"`
	ReviewUsage         agent.ProviderUsage              `json:"review_usage"`
	QueryUsage          agent.ProviderUsage              `json:"query_usage"`
	Stages              []Stage                          `json:"stages"`
	Cleanup             agent.SourceCleanupReceipt       `json:"cleanup"`
	Query               agent.QueryResult                `json:"-"`
}

type Runner struct {
	sources    SourceStore
	epub       EPUBParser
	drafter    Drafter
	ledger     ClaimLedger
	reviewer   Reviewer
	reconciler Reconciler
	query      QueryAgent
	approvals  PlanApprovalSource
	clock      Clock
}

func NewRunner(dependencies Dependencies) (*Runner, error) {
	if dependencies.Sources == nil || dependencies.EPUB == nil || dependencies.Drafter == nil ||
		dependencies.Ledger == nil || dependencies.Reviewer == nil || dependencies.Reconciler == nil ||
		dependencies.Query == nil || dependencies.Approvals == nil || dependencies.Clock == nil {
		return nil, ErrInvalidRunner
	}
	return &Runner{
		sources: dependencies.Sources, epub: dependencies.EPUB, drafter: dependencies.Drafter,
		ledger: dependencies.Ledger, reviewer: dependencies.Reviewer,
		reconciler: dependencies.Reconciler, query: dependencies.Query,
		approvals: dependencies.Approvals, clock: dependencies.Clock,
	}, nil
}

func (runner *Runner) Run(ctx context.Context, request Request) (Result, error) {
	if runner == nil || ctx == nil || !validRequest(request) {
		return Result{}, ErrInvalidRunner
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	result := Result{
		Version: ResultVersion, RunID: request.RunID, Status: StatusSucceeded,
		Question: request.Query.Question, Stages: []Stage{},
	}
	result.SourceSHA256 = payloadSHA256(request.Payload)
	cleanupPending := false
	defer func() {
		if cleanupPending {
			_, _ = runner.sources.ReleaseJob(request.JobID)
		}
	}()

	started := runner.clock.Now()
	put, err := runner.sources.Put(ctx, agent.SourcePutRequest{
		Version: agent.SourcePutRequestVersion, JobID: request.JobID, SourceID: request.SourceID,
		ExpectedSHA256: result.SourceSHA256, MediaType: "application/epub+zip", FileName: request.FileName,
	}, bytes.NewReader(request.Payload))
	result.Stages = append(result.Stages, measuredStage("source_store", started, runner.clock.Now()))
	if err != nil || put.Reference.SHA256 != result.SourceSHA256 {
		return Result{}, runError("store source", err)
	}
	cleanupPending = true

	started = runner.clock.Now()
	document, parseReceipt, err := runner.epub.Parse(ctx, request.JobID, request.SourceID)
	result.Stages = append(result.Stages, measuredStage("epub_parse", started, runner.clock.Now()))
	if err != nil || document.Validate() != nil || parseReceipt.Validate() != nil {
		return Result{}, runError("parse EPUB", err)
	}
	result.DocumentSHA256, result.Parse = document.SHA256, parseReceipt

	started = runner.clock.Now()
	coverage, err := agent.NewReadCoverage(document)
	if err != nil {
		return Result{}, runError("create coverage", err)
	}
	batches := make([]agent.DraftClaimBatch, 0)
	reviews := make([]agent.AssimilationReviewArtifact, 0)
	for {
		extent, nextErr := coverage.Next(request.ExtentLimits)
		if errors.Is(nextErr, agent.ErrReadCoverageComplete) {
			break
		}
		if nextErr != nil {
			return Result{}, runError("schedule extent", nextErr)
		}
		if _, err := runner.drafter.Draft(ctx, agent.AssimilationDraftRequest{
			JobID: request.JobID, Database: request.Query.Authorization.AuthorizedDatabases[0],
			SourceID: request.SourceID, SourceLocator: request.FileName,
			SourceSHA256: result.SourceSHA256, Extent: extent,
		}); err != nil {
			return Result{}, runError("draft extent", err)
		}
		batch, err := runner.ledger.ReadExtent(request.JobID, extent.Sequence)
		if err != nil {
			return Result{}, runError("read claim ledger", err)
		}
		batches = append(batches, batch)
		result.Claims += uint64(len(batch.Claims))
		result.DraftUsage = addUsage(result.DraftUsage, batch.Usage)
		if len(batch.Claims) > 0 {
			artifact, err := runner.reviewer.Review(ctx, agent.AssimilationReviewRequest{
				Batch: batch, Extent: extent, PeerClaims: []agent.DraftClaim{},
			})
			if err != nil || artifact.Status != agent.AssimilationReviewAccepted {
				return Result{}, runError("review extent", err)
			}
			reviews = append(reviews, artifact)
			result.ReviewUsage = addUsage(result.ReviewUsage, artifact.Usage)
		}
		if _, err := coverage.Acknowledge(extent.Sequence, extent.SHA256); err != nil {
			return Result{}, runError("acknowledge extent", err)
		}
	}
	result.Stages = append(result.Stages, measuredStage("assimilate_extents", started, runner.clock.Now()))
	result.Coverage = coverage.Receipt()
	result.Extents, result.Reviews = uint64(len(batches)), uint64(len(reviews))
	result.DraftProviderCalls, result.ReviewProviderCalls = result.Extents, result.Reviews

	started = runner.clock.Now()
	plan, err := runner.reconciler.Prepare(ctx, agent.AssimilationReconciliationRequest{
		SubmissionID: request.SubmissionID, SourceID: request.SourceID,
		SourceLocator: request.FileName, SourceSHA256: result.SourceSHA256,
		Coverage: result.Coverage, Batches: batches, Reviews: reviews,
	})
	result.Stages = append(result.Stages, measuredStage("plan_review", started, runner.clock.Now()))
	if err != nil {
		return Result{}, runError("review assimilation plan", err)
	}
	result.PlanID, result.PlanSHA256 = plan.PlanID, plan.SHA256

	started = runner.clock.Now()
	approval, err := runner.approvals.ApproveAssimilationPlan(ctx, plan)
	result.Stages = append(result.Stages, measuredStage("user_approval", started, runner.clock.Now()))
	if err != nil {
		return Result{}, runError("approve assimilation plan", err)
	}

	started = runner.clock.Now()
	receipt, err := runner.reconciler.SubmitApproved(ctx, plan, approval)
	result.Stages = append(result.Stages, measuredStage("submit_reconcile", started, runner.clock.Now()))
	if err != nil || receipt.Status != protocolmsql.AssimilationCommitted {
		return Result{}, runError("submit assimilation plan", err)
	}
	result.SourceReceipt = receipt

	started = runner.clock.Now()
	queryResult, err := runner.query.Query(ctx, request.Query)
	result.Stages = append(result.Stages, measuredStage("query", started, runner.clock.Now()))
	if err != nil || queryResult.Validate() != nil {
		return Result{}, runError("query assimilated fact", err)
	}
	result.Query, result.Answer = queryResult, queryResult.Answer
	for _, evidence := range queryResult.Evidence {
		result.SelectEvidenceRows += uint64(len(evidence.Result.Rows))
	}
	result.QueryProviderCalls = queryResult.Trace.Summary.KindCounts[agent.TraceKindProvider]
	result.QueryUsage = agent.ProviderUsage{
		InputTokens:       queryResult.Trace.Summary.InputTokens,
		CachedInputTokens: queryResult.Trace.Summary.CachedInputTokens,
		OutputTokens:      queryResult.Trace.Summary.OutputTokens,
		TotalTokens:       queryResult.Trace.Summary.TotalTokens,
	}

	started = runner.clock.Now()
	cleanup, err := runner.sources.ReleaseJob(request.JobID)
	result.Stages = append(result.Stages, measuredStage("cleanup", started, runner.clock.Now()))
	if err != nil {
		return Result{}, runError("cleanup source", err)
	}
	cleanupPending, result.Cleanup = false, cleanup
	if err := result.Validate(); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (result Result) Validate() error {
	if result.Version != ResultVersion || result.Status != StatusSucceeded ||
		strings.TrimSpace(result.RunID) == "" || !validSHA256(result.SourceSHA256) ||
		!validSHA256(result.DocumentSHA256) || result.Parse.Validate() != nil ||
		result.Parse.SourceSHA256 != result.SourceSHA256 || result.Parse.IRSHA256 != result.DocumentSHA256 ||
		result.Coverage.Version != agent.ReadCoverageReceiptVersion || !result.Coverage.Complete ||
		result.Coverage.DocumentSHA256 != result.DocumentSHA256 || result.Coverage.TotalNodes == 0 ||
		result.Coverage.CoveredNodes != result.Coverage.TotalNodes || result.Extents == 0 ||
		result.Claims == 0 || result.Reviews == 0 || result.DraftProviderCalls != result.Extents ||
		result.ReviewProviderCalls != result.Reviews || result.QueryProviderCalls < 2 ||
		!validSHA256(result.PlanSHA256) || result.PlanID == "" ||
		result.SourceReceipt.Validate() != nil || result.SourceReceipt.Status != protocolmsql.AssimilationCommitted ||
		result.SourceReceipt.PlanID != result.PlanID || result.SourceReceipt.PlanSHA256 != result.PlanSHA256 ||
		strings.TrimSpace(result.Question) == "" || strings.TrimSpace(result.Answer) == "" ||
		result.SelectEvidenceRows == 0 || result.Query.Validate() != nil ||
		!validUsage(result.DraftUsage) || !validUsage(result.ReviewUsage) || !validUsage(result.QueryUsage) ||
		len(result.Stages) != 8 || result.Cleanup.ReferencesRemoved == 0 || result.Cleanup.ObjectsRemoved == 0 {
		return ErrRunChain
	}
	wantStages := []string{
		"source_store", "epub_parse", "assimilate_extents", "plan_review",
		"user_approval", "submit_reconcile", "query", "cleanup",
	}
	for index, stage := range result.Stages {
		if stage.Name != wantStages[index] || stage.DurationMicros == 0 {
			return ErrRunChain
		}
	}
	if !receiptEvidenceMatchesQuery(result.SourceReceipt, result.Query) {
		return ErrRunChain
	}
	return nil
}

func validRequest(request Request) bool {
	return strings.TrimSpace(request.RunID) != "" && strings.TrimSpace(request.JobID) != "" &&
		strings.TrimSpace(request.SourceID) != "" && strings.TrimSpace(request.FileName) != "" &&
		len(request.Payload) > 0 && strings.TrimSpace(request.SubmissionID) != "" &&
		request.ExtentLimits.MaxNodes > 0 && request.ExtentLimits.MaxUTF8Bytes > 0 &&
		len(request.Query.Authorization.AuthorizedDatabases) == 1
}

func receiptEvidenceMatchesQuery(receipt protocolmsql.AssimilationReceipt, query agent.QueryResult) bool {
	objects := make(map[string]struct{})
	for _, statement := range receipt.Statements {
		for _, objectID := range statement.ObjectIDs {
			objects[objectID] = struct{}{}
		}
	}
	for _, evidence := range query.Evidence {
		for _, row := range evidence.Result.Rows {
			rowID, _ := row["row_id"].(string)
			if _, found := objects[rowID]; found {
				return true
			}
		}
	}
	return false
}

func addUsage(left, right agent.ProviderUsage) agent.ProviderUsage {
	return agent.ProviderUsage{
		InputTokens:       left.InputTokens + right.InputTokens,
		CachedInputTokens: left.CachedInputTokens + right.CachedInputTokens,
		OutputTokens:      left.OutputTokens + right.OutputTokens,
		TotalTokens:       left.TotalTokens + right.TotalTokens,
	}
}

func validUsage(usage agent.ProviderUsage) bool {
	return usage.CachedInputTokens <= usage.InputTokens &&
		usage.TotalTokens == usage.InputTokens+usage.OutputTokens
}

func measuredStage(name string, started, finished time.Time) Stage {
	duration := uint64(0)
	if !started.IsZero() && !finished.Before(started) {
		duration = uint64(finished.Sub(started) / time.Microsecond)
	}
	return Stage{Name: name, DurationMicros: duration}
}

func payloadSHA256(payload []byte) string {
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size
}

func runError(stage string, err error) error {
	if err == nil {
		err = ErrRunChain
	}
	return fmt.Errorf("%w: %s: %v", ErrRunChain, stage, err)
}
