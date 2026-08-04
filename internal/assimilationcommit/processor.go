package assimilationcommit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

const (
	recordBucket      = "assimilation.msql.records.v1"
	receiptBucket     = "assimilation.msql.receipts.v1"
	maximumRecordSize = 96 << 10
)

type Executor interface {
	ExecuteMSQL(context.Context, protocolmsql.Request) (protocolmsql.Envelope, error)
}

type ExecutorFunc func(context.Context, protocolmsql.Request) (protocolmsql.Envelope, error)

func (function ExecutorFunc) ExecuteMSQL(
	ctx context.Context,
	request protocolmsql.Request,
) (protocolmsql.Envelope, error) {
	return function(ctx, request)
}

type Processor struct {
	mu       sync.Mutex
	store    store.Store
	executor Executor
}

type recordStatus string

const (
	recordProcessing recordStatus = "processing"
	recordCompleted  recordStatus = "completed"
)

type record struct {
	PlanSHA256     string                             `json:"plan_sha256"`
	Database       string                             `json:"database"`
	SourceID       string                             `json:"source_id"`
	SourceSHA256   string                             `json:"source_sha256"`
	DocumentSHA256 string                             `json:"document_sha256"`
	Evidence       *protocolmsql.AssimilationEvidence `json:"evidence,omitempty"`
	Status         recordStatus                       `json:"status"`
	Receipt        *protocolmsql.AssimilationReceipt  `json:"receipt,omitempty"`
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return "assimilation commit: " + err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func New(database store.Store, executor Executor) *Processor {
	return &Processor{store: database, executor: executor}
}

func (processor *Processor) SubmitAssimilation(
	ctx context.Context,
	plan protocolmsql.AssimilationPlan,
) (protocolmsql.AssimilationReceipt, error) {
	if processor == nil || processor.store == nil || processor.executor == nil {
		return protocolmsql.AssimilationReceipt{}, commitError(result.CodeInternal, "processor is not configured")
	}
	if err := plan.Validate(); err != nil {
		return protocolmsql.AssimilationReceipt{}, commitError(result.CodeValidation, "plan is invalid")
	}
	processor.mu.Lock()
	defer processor.mu.Unlock()

	if receipt, found, err := processor.lookup(ctx, plan.PlanID, plan.SHA256); err != nil {
		return protocolmsql.AssimilationReceipt{}, err
	} else if found {
		receipt.Replayed = true
		return receipt, nil
	}
	if err := processor.reserve(ctx, plan); err != nil {
		return protocolmsql.AssimilationReceipt{}, err
	}

	request := assimilationRequest(plan)
	envelope, executeErr := processor.executor.ExecuteMSQL(ctx, request)
	if executeErr != nil {
		receipt := inDoubtReceipt(plan)
		if err := processor.complete(ctx, plan, receipt); err != nil {
			return protocolmsql.AssimilationReceipt{}, err
		}
		return receipt, nil
	}
	if err := envelope.Validate(); err != nil {
		receipt := inDoubtReceipt(plan)
		if completeErr := processor.complete(ctx, plan, receipt); completeErr != nil {
			return protocolmsql.AssimilationReceipt{}, completeErr
		}
		return receipt, nil
	}
	if !envelope.OK {
		if err := processor.release(ctx, plan); err != nil {
			return protocolmsql.AssimilationReceipt{}, err
		}
		return protocolmsql.AssimilationReceipt{}, envelopeFailure(envelope)
	}
	receipt, err := committedReceipt(plan, envelope)
	if err != nil {
		inDoubt := inDoubtReceipt(plan)
		if completeErr := processor.complete(ctx, plan, inDoubt); completeErr != nil {
			return protocolmsql.AssimilationReceipt{}, completeErr
		}
		return inDoubt, nil
	}
	if err := processor.complete(ctx, plan, receipt); err != nil {
		return protocolmsql.AssimilationReceipt{}, err
	}
	return receipt, nil
}

func (processor *Processor) AssimilationReceipt(
	ctx context.Context,
	database string,
	receiptID string,
) (protocolmsql.AssimilationReceipt, error) {
	if processor == nil || processor.store == nil || strings.TrimSpace(database) == "" ||
		strings.TrimSpace(receiptID) == "" || len(receiptID) > 200 {
		return protocolmsql.AssimilationReceipt{}, commitError(result.CodeValidation, "bounded Database and receipt ID are required")
	}
	processor.mu.Lock()
	defer processor.mu.Unlock()
	tx, err := processor.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return protocolmsql.AssimilationReceipt{}, commitError(result.CodeInternal, "begin receipt read: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	encoded, err := tx.Get(ctx, receiptBucket, receiptID)
	if errors.Is(err, store.ErrNotFound) {
		recordBytes, recordErr := tx.Get(ctx, recordBucket, receiptID)
		if errors.Is(recordErr, store.ErrNotFound) {
			return protocolmsql.AssimilationReceipt{}, commitError(result.CodeNotFound, "assimilation receipt was not found")
		}
		if recordErr != nil {
			return protocolmsql.AssimilationReceipt{}, commitError(result.CodeInternal, "read assimilation record: %v", recordErr)
		}
		var current record
		if json.Unmarshal(recordBytes, &current) != nil || current.Status != recordProcessing {
			return protocolmsql.AssimilationReceipt{}, commitError(result.CodeInternal, "assimilation record is corrupt")
		}
		if !strings.EqualFold(current.Database, database) {
			return protocolmsql.AssimilationReceipt{}, commitError(result.CodePermissionDenied, "assimilation receipt belongs to another Database")
		}
		return protocolmsql.AssimilationReceipt{
			Version: protocolmsql.AssimilationReceiptVersion, ReceiptID: receiptID,
			PlanID: receiptID, PlanSHA256: current.PlanSHA256, Database: current.Database,
			SourceID: current.SourceID, SourceSHA256: current.SourceSHA256,
			DocumentSHA256: current.DocumentSHA256,
			Evidence:       cloneAssimilationEvidence(current.Evidence),
			Status:         protocolmsql.AssimilationInDoubt, Statements: []protocolmsql.AssimilationStatementReceipt{},
		}, nil
	}
	if err != nil {
		return protocolmsql.AssimilationReceipt{}, commitError(result.CodeInternal, "read assimilation receipt: %v", err)
	}
	var receipt protocolmsql.AssimilationReceipt
	if json.Unmarshal(encoded, &receipt) != nil || receipt.Validate() != nil {
		return protocolmsql.AssimilationReceipt{}, commitError(result.CodeInternal, "assimilation receipt is corrupt")
	}
	if !strings.EqualFold(receipt.Database, database) {
		return protocolmsql.AssimilationReceipt{}, commitError(result.CodePermissionDenied, "assimilation receipt belongs to another Database")
	}
	return receipt, nil
}

func assimilationRequest(plan protocolmsql.AssimilationPlan) protocolmsql.Request {
	statements := make([]protocolmsql.StatementInput, 0, len(plan.Proposal.Statements)+2)
	sources := make([]string, 0, len(plan.Proposal.Statements)+2)
	authorization := protocolmsql.Authorization{
		Version: protocolmsql.AuthorizationVersion, Actor: plan.Proposal.Author,
		AuthorizedDatabases: []string{plan.Proposal.Database}, DefaultLevel: protocolmsql.LevelWrite,
		DatabaseLevels: map[string]protocolmsql.RiskLevel{plan.Proposal.Database: protocolmsql.LevelWrite},
	}
	sources = append(sources, "BEGIN")
	statements = append(statements, protocolmsql.StatementInput{Authorization: authorization})
	for _, statement := range plan.Proposal.Statements {
		sources = append(sources, statement.MSQL)
		statements = append(statements, protocolmsql.StatementInput{
			Parameters: cloneParameters(statement.Parameters), Mutation: cloneMutation(statement.Mutation),
			Authorization: authorization,
		})
	}
	sources = append(sources, "COMMIT")
	statements = append(statements, protocolmsql.StatementInput{Authorization: authorization})
	return protocolmsql.Request{
		Version: protocolmsql.RequestVersion, Source: strings.Join(sources, ";"), Statements: statements,
	}
}

func committedReceipt(
	plan protocolmsql.AssimilationPlan,
	envelope protocolmsql.Envelope,
) (protocolmsql.AssimilationReceipt, error) {
	want := len(plan.Proposal.Statements) + 2
	if len(envelope.Results) != want || envelope.Results[0].Statement != "BEGIN" ||
		envelope.Results[0].Status != protocolmsql.Status(result.StatusSucceeded) ||
		envelope.Results[len(envelope.Results)-1].Statement != "COMMIT" ||
		envelope.Results[len(envelope.Results)-1].Status != protocolmsql.Status(result.StatusSucceeded) {
		return protocolmsql.AssimilationReceipt{}, commitError(result.CodeInternal, "transaction result count is invalid")
	}
	receipt := receiptBase(plan, protocolmsql.AssimilationCommitted)
	for index, statement := range envelope.Results[1 : len(envelope.Results)-1] {
		if statement.Status != protocolmsql.Status(result.StatusSucceeded) || statement.AffectedRows == 0 ||
			statement.Revision == nil || *statement.Revision == 0 ||
			statement.CommitSequence == nil || *statement.CommitSequence == 0 {
			return protocolmsql.AssimilationReceipt{}, commitError(result.CodeInternal, "transaction statement result is invalid")
		}
		objectIDs, err := assimilationStatementObjectIDs(statement)
		if err != nil {
			return protocolmsql.AssimilationReceipt{}, err
		}
		receipt.Statements = append(receipt.Statements, protocolmsql.AssimilationStatementReceipt{
			Index: index, Kind: statement.Statement, AffectedRows: statement.AffectedRows,
			ObjectIDs: objectIDs,
			Revision:  cloneUint64(statement.Revision), CommitSequence: cloneUint64(statement.CommitSequence),
		})
	}
	if receipt.Validate() != nil {
		return protocolmsql.AssimilationReceipt{}, commitError(result.CodeInternal, "committed receipt is invalid")
	}
	return receipt, nil
}

func receiptBase(plan protocolmsql.AssimilationPlan, status string) protocolmsql.AssimilationReceipt {
	return protocolmsql.AssimilationReceipt{
		Version: protocolmsql.AssimilationReceiptVersion, ReceiptID: plan.PlanID,
		PlanID: plan.PlanID, PlanSHA256: plan.SHA256, Database: plan.Proposal.Database,
		SourceID: plan.Proposal.SourceID, SourceSHA256: plan.Proposal.SourceSHA256,
		DocumentSHA256: plan.Proposal.DocumentSHA256, Status: status,
		Evidence:   cloneAssimilationEvidence(plan.Proposal.Evidence),
		Statements: []protocolmsql.AssimilationStatementReceipt{},
	}
}

func assimilationStatementObjectIDs(statement protocolmsql.StatementResult) ([]string, error) {
	key := "row_id"
	if statement.Statement == "RELATE" || statement.Statement == "UNRELATE" {
		key = "relation_id"
	}
	if uint64(len(statement.Rows)) != statement.AffectedRows {
		return nil, commitError(result.CodeInternal, "transaction statement object inventory is incomplete")
	}
	objectIDs := make([]string, len(statement.Rows))
	seen := make(map[string]struct{}, len(statement.Rows))
	for index, row := range statement.Rows {
		objectID, ok := row[key].(string)
		if !ok || strings.TrimSpace(objectID) == "" || len(objectID) > 200 {
			return nil, commitError(result.CodeInternal, "transaction statement object inventory is invalid")
		}
		if _, duplicate := seen[objectID]; duplicate {
			return nil, commitError(result.CodeInternal, "transaction statement object inventory contains duplicates")
		}
		seen[objectID] = struct{}{}
		objectIDs[index] = objectID
	}
	return objectIDs, nil
}

func cloneAssimilationEvidence(
	evidence *protocolmsql.AssimilationEvidence,
) *protocolmsql.AssimilationEvidence {
	if evidence == nil {
		return nil
	}
	cloned := *evidence
	cloned.Reviewers = append([]string(nil), evidence.Reviewers...)
	cloned.ReviewArtifactSHA256s = append([]string(nil), evidence.ReviewArtifactSHA256s...)
	return &cloned
}

func inDoubtReceipt(plan protocolmsql.AssimilationPlan) protocolmsql.AssimilationReceipt {
	return receiptBase(plan, protocolmsql.AssimilationInDoubt)
}

func (processor *Processor) lookup(
	ctx context.Context,
	id string,
	digest string,
) (protocolmsql.AssimilationReceipt, bool, error) {
	tx, err := processor.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return protocolmsql.AssimilationReceipt{}, false, commitError(result.CodeInternal, "begin assimilation lookup: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	encoded, err := tx.Get(ctx, recordBucket, id)
	if errors.Is(err, store.ErrNotFound) {
		return protocolmsql.AssimilationReceipt{}, false, nil
	}
	if err != nil {
		return protocolmsql.AssimilationReceipt{}, false, commitError(result.CodeInternal, "read assimilation record: %v", err)
	}
	var current record
	if json.Unmarshal(encoded, &current) != nil || current.PlanSHA256 == "" {
		return protocolmsql.AssimilationReceipt{}, false, commitError(result.CodeInternal, "assimilation record is corrupt")
	}
	if current.PlanSHA256 != digest {
		return protocolmsql.AssimilationReceipt{}, false, commitError(result.CodeRevisionConflict, "assimilation plan ID is already used by different content")
	}
	if current.Status == recordProcessing || current.Receipt == nil {
		return protocolmsql.AssimilationReceipt{
			Version: protocolmsql.AssimilationReceiptVersion, ReceiptID: id, PlanID: id,
			PlanSHA256: digest, Database: current.Database, SourceID: current.SourceID,
			SourceSHA256: current.SourceSHA256, DocumentSHA256: current.DocumentSHA256,
			Evidence: cloneAssimilationEvidence(current.Evidence),
			Status:   protocolmsql.AssimilationInDoubt, Statements: []protocolmsql.AssimilationStatementReceipt{},
		}, true, nil
	}
	return *current.Receipt, true, nil
}

func (processor *Processor) reserve(ctx context.Context, plan protocolmsql.AssimilationPlan) error {
	current := record{
		PlanSHA256: plan.SHA256, Database: plan.Proposal.Database,
		SourceID: plan.Proposal.SourceID, SourceSHA256: plan.Proposal.SourceSHA256,
		DocumentSHA256: plan.Proposal.DocumentSHA256, Status: recordProcessing,
		Evidence: cloneAssimilationEvidence(plan.Proposal.Evidence),
	}
	encoded, _ := json.Marshal(current)
	tx, err := processor.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return commitError(result.CodeInternal, "begin assimilation reservation: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if existing, getErr := tx.Get(ctx, recordBucket, plan.PlanID); getErr == nil {
		var found record
		if json.Unmarshal(existing, &found) != nil || found.PlanSHA256 != plan.SHA256 {
			return commitError(result.CodeRevisionConflict, "assimilation plan ID is already reserved")
		}
		return commitError(result.CodeRevisionConflict, "assimilation plan is already processing")
	} else if !errors.Is(getErr, store.ErrNotFound) {
		return commitError(result.CodeInternal, "read assimilation reservation: %v", getErr)
	}
	if err := tx.Put(ctx, recordBucket, plan.PlanID, encoded); err != nil {
		return commitError(result.CodeInternal, "reserve assimilation plan: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return commitError(result.CodeInternal, "commit assimilation reservation: %v", err)
	}
	return nil
}

func (processor *Processor) release(ctx context.Context, plan protocolmsql.AssimilationPlan) error {
	tx, err := processor.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return commitError(result.CodeInternal, "begin assimilation reservation release: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.Delete(ctx, recordBucket, plan.PlanID); err != nil {
		return commitError(result.CodeInternal, "release assimilation reservation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return commitError(result.CodeInternal, "commit assimilation reservation release: %v", err)
	}
	return nil
}

func (processor *Processor) complete(
	ctx context.Context,
	plan protocolmsql.AssimilationPlan,
	receipt protocolmsql.AssimilationReceipt,
) error {
	if receipt.Validate() != nil {
		return commitError(result.CodeInternal, "assimilation receipt is invalid")
	}
	receipt.Replayed = false
	receiptBytes, err := json.Marshal(receipt)
	if err != nil || len(receiptBytes) > maximumRecordSize {
		return commitError(result.CodeOutputTruncated, "assimilation receipt exceeds its budget")
	}
	current := record{
		PlanSHA256: plan.SHA256, Database: plan.Proposal.Database,
		SourceID: plan.Proposal.SourceID, SourceSHA256: plan.Proposal.SourceSHA256,
		DocumentSHA256: plan.Proposal.DocumentSHA256,
		Evidence:       cloneAssimilationEvidence(plan.Proposal.Evidence),
		Status:         recordCompleted, Receipt: &receipt,
	}
	recordBytes, _ := json.Marshal(current)
	tx, err := processor.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return commitError(result.CodeInternal, "begin assimilation receipt commit: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.Put(ctx, recordBucket, plan.PlanID, recordBytes); err != nil {
		return commitError(result.CodeInternal, "complete assimilation plan: %v", err)
	}
	if err := tx.Put(ctx, receiptBucket, receipt.ReceiptID, receiptBytes); err != nil {
		return commitError(result.CodeInternal, "write assimilation receipt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return commitError(result.CodeInternal, "commit assimilation receipt: %v", err)
	}
	return nil
}

func envelopeFailure(envelope protocolmsql.Envelope) error {
	if envelope.Error != nil {
		return commitError(result.Code(envelope.Error.Code), "transaction failed: %s", envelope.Error.Message)
	}
	for _, statement := range envelope.Results {
		if statement.Error != nil {
			return commitError(result.Code(statement.Error.Code), "transaction failed: %s", statement.Error.Message)
		}
	}
	return commitError(result.CodeInternal, "transaction failed")
}

func cloneParameters(value protocolmsql.Parameters) protocolmsql.Parameters {
	return protocolmsql.Parameters{
		Named: cloneValues(value.Named), Positional: cloneSlice(value.Positional),
	}
}

func cloneValues(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	cloned := make(map[string]any, len(value))
	for key, item := range value {
		cloned[key] = cloneAny(item)
	}
	return cloned
}

func cloneSlice(value []any) []any {
	if value == nil {
		return nil
	}
	cloned := make([]any, len(value))
	for index := range value {
		cloned[index] = cloneAny(value[index])
	}
	return cloned
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneValues(typed)
	case []any:
		return cloneSlice(typed)
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}

func cloneMutation(value protocolmsql.MutationOptions) protocolmsql.MutationOptions {
	value.SourceRevisions = cloneMap(value.SourceRevisions)
	value.RouteLeafIDs = append([]string(nil), value.RouteLeafIDs...)
	value.TargetRouteLeafIDs = cloneStrings(value.TargetRouteLeafIDs)
	value.RelationTargetOrdinals = cloneMap(value.RelationTargetOrdinals)
	value.RouteUpdates = append([]protocolmsql.RouteUpdate(nil), value.RouteUpdates...)
	return value
}

func cloneMap[K comparable, V any](value map[K]V) map[K]V {
	if value == nil {
		return nil
	}
	cloned := make(map[K]V, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func cloneStrings(value [][]string) [][]string {
	if value == nil {
		return nil
	}
	cloned := make([][]string, len(value))
	for index := range value {
		cloned[index] = append([]string(nil), value[index]...)
	}
	return cloned
}

func cloneUint64(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func commitError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
