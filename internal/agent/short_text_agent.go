package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

const (
	ShortTextPlanVersion      = "memora.short-text-plan/v1"
	ShortTextReceiptVersion   = "memora.short-text-receipt/v1"
	ShortTextContextVersion   = "memora.short-text-context/v1"
	ShortTextProposeToolName  = "propose_short_write"
	ShortTextSystemPrompt     = "Memora short-text writer v1. Read the complete bounded source and navigation-only Bootstrap. Propose exactly one parameterized INSERT that stores one complete, independently editable semantic module in an existing Table and existing Route Leaf. Use the required propose_short_write tool. Do not create Schema or Route, update/delete existing Rows, copy raw chunks, or answer directly."
	shortTextToolArgumentsMax = 64 << 10
)

var (
	ErrInvalidShortTextDependencies = errors.New("Short Text Agent dependencies are invalid")
	ErrInvalidShortTextRequest      = errors.New("Short Text Agent request is invalid")
	ErrInvalidShortTextToolCall     = errors.New("Short Text Agent tool call is invalid")
	ErrInvalidShortTextPlan         = errors.New("Short Text Agent plan is invalid")
	ErrInvalidShortTextReceipt      = errors.New("Short Text Agent receipt is invalid")
	ErrShortTextCommit              = errors.New("Short Text Agent commit failed")
	ErrShortTextVerification        = errors.New("Short Text Agent commit is not verified")
)

type ShortTextStatus string

const (
	ShortTextStatusVerified            ShortTextStatus = "verified"
	ShortTextStatusCommitFailed        ShortTextStatus = "commit_failed"
	ShortTextStatusCommittedUnverified ShortTextStatus = "committed_unverified"
)

type ShortTextAgentDependencies struct {
	MSQL     MSQLExecutor
	Provider Provider
	Clock    QueryClock
}

type ShortTextAgentConfig struct {
	Model              string
	WriteProfile       WriteProfile
	BootstrapBudget    BootstrapBudget
	BootstrapProfile   BootstrapProfile
	MaxSourceUTF8Bytes uint64
	MaxOutputTokens    uint64
}

type ShortTextRequest struct {
	Identity      TraceIdentity `json:"-"`
	Intent        string        `json:"intent"`
	Title         string        `json:"title"`
	Content       string        `json:"content"`
	SourceID      string        `json:"source_id"`
	SourceLocator string        `json:"source_locator"`
}

type ShortTextPlan struct {
	Version             string        `json:"version"`
	Database            string        `json:"database"`
	Table               string        `json:"table"`
	SourceContentSHA256 string        `json:"source_content_sha256"`
	Proposal            WriteProposal `json:"proposal"`
	SHA256              string        `json:"sha256"`
	Trace               TraceEnvelope `json:"trace"`
}

func (plan ShortTextPlan) Validate() error {
	if plan.Version != ShortTextPlanVersion || !validShortIdentifier(plan.Database) ||
		!validShortIdentifier(plan.Table) || !validWriteSHA256(plan.SourceContentSHA256) ||
		plan.Proposal.Version != WriteProposalVersion || !validWriteSHA256(plan.Proposal.SHA256) ||
		plan.Trace.Validate() != nil || !validWriteSHA256(plan.SHA256) {
		return ErrInvalidShortTextPlan
	}
	digest, err := shortTextPlanDigest(plan)
	if err != nil || digest != plan.SHA256 {
		return ErrInvalidShortTextPlan
	}
	return nil
}

type ShortTextReceipt struct {
	Version        string                `json:"version"`
	Status         ShortTextStatus       `json:"status"`
	ProposalSHA256 string                `json:"proposal_sha256"`
	RowID          string                `json:"row_id,omitempty"`
	Revision       uint64                `json:"revision,omitempty"`
	CommitSequence uint64                `json:"commit_sequence,omitempty"`
	Commit         protocolmsql.Envelope `json:"commit"`
	Verification   *SelectEvidence       `json:"verification,omitempty"`
}

func (receipt ShortTextReceipt) Validate() error {
	if receipt.Version != ShortTextReceiptVersion || !validWriteSHA256(receipt.ProposalSHA256) ||
		receipt.Commit.Validate() != nil {
		return ErrInvalidShortTextReceipt
	}
	switch receipt.Status {
	case ShortTextStatusCommitFailed:
		if receipt.Commit.OK || receipt.RowID != "" || receipt.Revision != 0 || receipt.CommitSequence != 0 ||
			receipt.Verification != nil {
			return ErrInvalidShortTextReceipt
		}
	case ShortTextStatusCommittedUnverified:
		if !receipt.Commit.OK || receipt.Verification != nil {
			return ErrInvalidShortTextReceipt
		}
	case ShortTextStatusVerified:
		if !receipt.Commit.OK || strings.TrimSpace(receipt.RowID) == "" || receipt.Revision == 0 ||
			receipt.CommitSequence == 0 || receipt.Verification == nil ||
			receipt.Verification.RequestID == "" || receipt.Verification.Result.Statement != "SELECT" ||
			receipt.Verification.Result.Status != protocolmsql.Status("succeeded") ||
			len(receipt.Verification.Result.Rows) != 1 ||
			receipt.Verification.Result.Rows[0]["row_id"] != receipt.RowID {
			return ErrInvalidShortTextReceipt
		}
	default:
		return ErrInvalidShortTextReceipt
	}
	return nil
}

type ShortTextAgent struct {
	runtime          *Runtime
	provider         *ProviderGateway
	clock            QueryClock
	write            *WriteGateway
	profile          WriteProfile
	model            string
	bootstrapBudget  BootstrapBudget
	bootstrapProfile BootstrapProfile
	maxSourceBytes   uint64
	maxOutputTokens  uint64
}

func NewShortTextAgent(
	dependencies ShortTextAgentDependencies,
	config ShortTextAgentConfig,
) (*ShortTextAgent, error) {
	if dependencies.MSQL == nil || dependencies.Provider == nil || dependencies.Clock == nil ||
		invalidProviderText(config.Model, 200, true) || config.WriteProfile.MaxStatements != 1 ||
		config.WriteProfile.MaxAffectedRows != 1 || config.MaxSourceUTF8Bytes < 1024 ||
		config.MaxSourceUTF8Bytes > 64<<10 || config.MaxOutputTokens < 1 || config.MaxOutputTokens > 8192 {
		return nil, ErrInvalidShortTextDependencies
	}
	runtime, err := New(Dependencies{MSQL: dependencies.MSQL})
	if err != nil {
		return nil, ErrInvalidShortTextDependencies
	}
	provider, err := NewProviderGateway(dependencies.Provider)
	if err != nil {
		return nil, ErrInvalidShortTextDependencies
	}
	write, err := NewWriteGateway(dependencies.MSQL, config.WriteProfile)
	if err != nil {
		return nil, ErrInvalidShortTextDependencies
	}
	profile, valid := ResolveBootstrapProfile(config.BootstrapProfile)
	if !valid || validateBootstrapRequest(context.Background(), BootstrapRequest{
		Query: "short-text-construction-probe", Authorization: shortTextReadAuthorization(config.WriteProfile),
		Budget: config.BootstrapBudget, Profile: profile,
	}) != nil {
		return nil, ErrInvalidShortTextDependencies
	}
	return &ShortTextAgent{
		runtime: runtime, provider: provider, clock: dependencies.Clock, write: write,
		profile: cloneWriteProfile(config.WriteProfile), model: config.Model,
		bootstrapBudget: config.BootstrapBudget, bootstrapProfile: profile,
		maxSourceBytes: config.MaxSourceUTF8Bytes, maxOutputTokens: config.MaxOutputTokens,
	}, nil
}

func (agent *ShortTextAgent) Draft(ctx context.Context, request ShortTextRequest) (ShortTextPlan, error) {
	if agent == nil || ctx == nil || !agent.validRequest(request) {
		return ShortTextPlan{}, ErrInvalidShortTextRequest
	}
	recorder, err := newTraceRecorder(request.Identity, nil)
	if err != nil {
		return ShortTextPlan{}, ErrInvalidShortTextRequest
	}
	readOnly := shortTextReadAuthorization(agent.profile)
	tracedMSQL := &queryTracedMSQL{executor: agent.runtime, recorder: recorder, clock: agent.clock, turn: 1}
	assembler, _ := NewBootstrapAssembler(tracedMSQL)
	bootstrapInput := BootstrapRequest{
		Query: request.Intent, Authorization: readOnly,
		Budget: agent.bootstrapBudget, Profile: agent.bootstrapProfile,
	}
	bootstrapStarted := queryNow(agent.clock)
	frame, bootstrapErr := assembler.Assemble(ctx, bootstrapInput)
	bootstrapFinished := queryNow(agent.clock)
	if traceErr := queryAppendBootstrap(
		recorder, bootstrapInput, frame, bootstrapStarted, bootstrapFinished, bootstrapErr,
	); traceErr != nil {
		return ShortTextPlan{}, traceErr
	}
	if bootstrapErr != nil {
		return ShortTextPlan{}, bootstrapErr
	}
	providerRequest, err := agent.providerRequest(request, frame)
	if err != nil {
		return ShortTextPlan{}, err
	}
	providerStarted := queryNow(agent.clock)
	response, providerErr := agent.provider.Complete(ctx, providerRequest)
	providerFinished := queryNow(agent.clock)
	if traceErr := queryAppendProvider(
		recorder, 1, providerRequest, response, providerStarted, providerFinished, providerErr,
	); traceErr != nil {
		return ShortTextPlan{}, traceErr
	}
	if providerErr != nil {
		return ShortTextPlan{}, providerErr
	}
	toolStarted := queryNow(agent.clock)
	arguments, decodeErr := agent.decodeToolResponse(response)
	var proposal WriteProposal
	contentHash := shortTextContentSHA256(request.Content)
	if decodeErr == nil {
		proposal, decodeErr = agent.write.Prepare(WriteDraft{
			ProposalID: arguments.ProposalID, Source: arguments.Source,
			Statements: []WriteStatement{{
				Parameters: protocolmsql.Parameters{Named: arguments.Named, Positional: arguments.Positional},
				Mutation: protocolmsql.MutationOptions{
					ExpectedSchemaVersion: arguments.ExpectedSchemaVersion, MaxAffectedRows: 1,
					Source: request.SourceID, Reason: arguments.Reason,
					SourceKind: protocolmsql.SourceDocumentAnchor, SourceLocator: request.SourceLocator,
					SourceContentHash: contentHash, RouteLeafIDs: arguments.RouteLeafIDs,
				},
			}},
		})
		if decodeErr != nil {
			decodeErr = fmt.Errorf("%w: %v", ErrInvalidShortTextToolCall, decodeErr)
		}
	}
	toolFinished := queryNow(agent.clock)
	if traceErr := shortTextAppendTool(
		recorder, response, proposal, toolStarted, toolFinished, decodeErr,
	); traceErr != nil {
		return ShortTextPlan{}, traceErr
	}
	if decodeErr != nil {
		return ShortTextPlan{}, decodeErr
	}
	plan := ShortTextPlan{
		Version: ShortTextPlanVersion, Database: arguments.Database, Table: arguments.Table,
		SourceContentSHA256: contentHash, Proposal: proposal, Trace: recorder.Snapshot(),
	}
	plan.SHA256, err = shortTextPlanDigest(plan)
	if err != nil || plan.Validate() != nil {
		return ShortTextPlan{}, ErrInvalidShortTextPlan
	}
	return plan, nil
}

func (agent *ShortTextAgent) Commit(
	ctx context.Context,
	plan ShortTextPlan,
	approval WriteApproval,
) (ShortTextReceipt, error) {
	if agent == nil || ctx == nil || plan.Validate() != nil ||
		!agent.databaseAllowed(plan.Database) {
		return ShortTextReceipt{}, ErrInvalidShortTextPlan
	}
	commit, err := agent.write.ExecuteApproved(ctx, plan.Proposal, approval)
	if err != nil {
		return ShortTextReceipt{}, err
	}
	receipt := ShortTextReceipt{
		Version: ShortTextReceiptVersion, ProposalSHA256: plan.Proposal.SHA256, Commit: commit,
	}
	if !commit.OK {
		receipt.Status = ShortTextStatusCommitFailed
		return receipt, ErrShortTextCommit
	}
	rowID, revision, sequence, validInsert := shortTextInsertReceipt(commit)
	receipt.Status = ShortTextStatusCommittedUnverified
	if !validInsert {
		return receipt, ErrShortTextVerification
	}
	receipt.RowID, receipt.Revision, receipt.CommitSequence = rowID, revision, sequence
	verification, verifyErr := agent.runtime.ExecuteMSQL(ctx, protocolmsql.Request{
		Version: protocolmsql.RequestVersion,
		Source: "SELECT * FROM " + shortTextQuoteIdentifier(plan.Database) + "." +
			shortTextQuoteIdentifier(plan.Table) + " WHERE row_id = :row LIMIT 1",
		Statements: []protocolmsql.StatementInput{{
			Parameters:    protocolmsql.Parameters{Named: map[string]any{"row": rowID}},
			Authorization: shortTextReadAuthorization(agent.profile),
		}},
	})
	if verifyErr != nil {
		return receipt, fmt.Errorf("%w: %v", ErrShortTextVerification, verifyErr)
	}
	evidence, validVerify := shortTextVerificationEvidence(verification, rowID, revision, sequence)
	if !validVerify {
		return receipt, ErrShortTextVerification
	}
	receipt.Status = ShortTextStatusVerified
	receipt.Verification = &evidence
	if receipt.Validate() != nil {
		return receipt, ErrInvalidShortTextReceipt
	}
	return receipt, nil
}

func (agent *ShortTextAgent) validRequest(request ShortTextRequest) bool {
	return validTraceIdentity(request.Identity) && strings.TrimSpace(request.Intent) != "" &&
		utf8.ValidString(request.Intent) && utf8.RuneCountInString(request.Intent) <= 256 &&
		validWriteText(request.Title, 500) && strings.TrimSpace(request.Content) != "" &&
		utf8.ValidString(request.Content) && uint64(len(request.Content)) <= agent.maxSourceBytes &&
		validWriteText(request.SourceID, 1000) && validWriteText(request.SourceLocator, 2048)
}

func (agent *ShortTextAgent) databaseAllowed(database string) bool {
	for _, allowed := range agent.profile.AuthorizedDatabases {
		if strings.EqualFold(strings.TrimSpace(allowed), strings.TrimSpace(database)) {
			return true
		}
	}
	return false
}

func shortTextAppendTool(
	recorder *TraceRecorder,
	response ProviderResponse,
	proposal WriteProposal,
	started, finished time.Time,
	operationErr error,
) error {
	input, _ := json.Marshal(response.Message)
	var output []byte
	if operationErr == nil {
		output, _ = json.Marshal(proposal)
	}
	status, errorCode := queryTraceStatus(operationErr, "SHORT_TEXT_TOOL_INVALID")
	_, err := recorder.Append(TraceDraft{
		Turn: 1, Kind: TraceKindTool, Operation: "tool.propose_short_write",
		StartedAt: started, FinishedAt: finished, Status: status, ErrorCode: errorCode,
		Input: DigestTracePayload(input), Output: DigestTracePayload(output),
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrQueryTrace, err)
	}
	return nil
}

func shortTextPlanDigest(plan ShortTextPlan) (string, error) {
	payload := struct {
		Version             string `json:"version"`
		Database            string `json:"database"`
		Table               string `json:"table"`
		SourceContentSHA256 string `json:"source_content_sha256"`
		ProposalVersion     string `json:"proposal_version"`
		ProposalID          string `json:"proposal_id"`
		ProfileID           string `json:"profile_id"`
		ProposalSHA256      string `json:"proposal_sha256"`
	}{
		Version: plan.Version, Database: plan.Database, Table: plan.Table,
		SourceContentSHA256: plan.SourceContentSHA256, ProposalVersion: plan.Proposal.Version,
		ProposalID: plan.Proposal.ProposalID, ProfileID: plan.Proposal.ProfileID,
		ProposalSHA256: plan.Proposal.SHA256,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func shortTextInsertReceipt(envelope protocolmsql.Envelope) (string, uint64, uint64, bool) {
	if !envelope.OK || len(envelope.Results) != 1 {
		return "", 0, 0, false
	}
	result := envelope.Results[0]
	if result.Statement != "INSERT" || result.Status != protocolmsql.Status("succeeded") || result.Error != nil ||
		result.AffectedRows != 1 || result.Revision == nil || result.CommitSequence == nil ||
		len(result.Rows) != 1 {
		return "", 0, 0, false
	}
	rowID, ok := result.Rows[0]["row_id"].(string)
	if !ok || strings.TrimSpace(rowID) == "" || *result.Revision == 0 || *result.CommitSequence == 0 {
		return "", 0, 0, false
	}
	return rowID, *result.Revision, *result.CommitSequence, true
}

func shortTextVerificationEvidence(
	envelope protocolmsql.Envelope,
	rowID string,
	revision, sequence uint64,
) (SelectEvidence, bool) {
	if !envelope.OK || len(envelope.Results) != 1 {
		return SelectEvidence{}, false
	}
	result := envelope.Results[0]
	if result.Statement != "SELECT" || result.Status != protocolmsql.Status("succeeded") ||
		result.Error != nil || len(result.Rows) != 1 || result.Rows[0]["row_id"] != rowID {
		return SelectEvidence{}, false
	}
	verifiedRevision, ok := shortTextUint64(result.Rows[0]["revision"])
	if !ok || verifiedRevision != revision {
		return SelectEvidence{}, false
	}
	verifiedSequence, ok := shortTextUint64(result.Rows[0]["commit_sequence"])
	if !ok || verifiedSequence != sequence {
		return SelectEvidence{}, false
	}
	return SelectEvidence{RequestID: envelope.RequestID, Result: cloneQueryStatement(result)}, true
}

func shortTextUint64(value any) (uint64, bool) {
	switch typed := value.(type) {
	case uint64:
		return typed, typed > 0
	case int64:
		return uint64(typed), typed > 0
	case float64:
		return uint64(typed), typed > 0 && typed == float64(uint64(typed))
	case json.Number:
		parsed, err := strconv.ParseUint(string(typed), 10, 64)
		return parsed, err == nil && parsed > 0
	default:
		return 0, false
	}
}

func shortTextReadAuthorization(profile WriteProfile) protocolmsql.Authorization {
	levels := make(map[string]protocolmsql.RiskLevel, len(profile.AuthorizedDatabases))
	for _, database := range profile.AuthorizedDatabases {
		levels[database] = protocolmsql.LevelRead
	}
	return protocolmsql.Authorization{
		Version: protocolmsql.AuthorizationVersion, Actor: profile.Actor,
		AuthorizedDatabases: append([]string(nil), profile.AuthorizedDatabases...),
		DefaultLevel:        protocolmsql.LevelRead, DatabaseLevels: levels,
	}
}

func shortTextContentSHA256(content string) string {
	digest := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validShortIdentifier(value string) bool {
	if strings.TrimSpace(value) != value || !validWriteText(value, 200) || strings.Contains(value, ".") {
		return false
	}
	return true
}

func shortTextQuoteIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}
