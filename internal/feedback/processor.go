package feedback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillwrite"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	eventBucket        = "feedback.events.v1"
	confirmationBucket = "feedback.confirmations.v1"
	processing         = "processing"
	completed          = "completed"
)

type eventRecord struct {
	Digest  string  `json:"digest"`
	Event   Event   `json:"event"`
	Receipt Receipt `json:"receipt"`
}

type confirmationRecord struct {
	Digest  string               `json:"digest"`
	Status  string               `json:"status"`
	Receipt *ConfirmationReceipt `json:"receipt,omitempty"`
}

type Processor struct {
	store  store.Store
	reader Reader
	tool   skillwrite.Tool
}

func New(database store.Store, reader Reader, tool skillwrite.Tool) *Processor {
	return &Processor{store: database, reader: reader, tool: tool}
}

func (processor *Processor) Record(ctx context.Context, event Event) (Receipt, error) {
	if err := validateEvent(event); err != nil {
		return Receipt{}, err
	}
	if processor == nil || processor.store == nil || processor.reader == nil {
		return Receipt{}, feedbackError(result.CodeInternal, "feedback processor is not configured")
	}
	digest, err := contentDigest(event)
	if err != nil {
		return Receipt{}, err
	}
	if receipt, found, lookupErr := processor.lookupEvent(ctx, event.EventID, digest); lookupErr != nil || found {
		if found {
			receipt.Replayed = true
		}
		return receipt, lookupErr
	}
	current, err := processor.reader.Get(ctx, event.Target.Database, event.Target.Table, event.Target.RowID)
	if err != nil {
		return Receipt{}, normalizeError(err, "read feedback target")
	}
	if current.Revision != event.Target.Revision {
		return Receipt{}, feedbackError(result.CodeRevisionConflict, "feedback target revision = %d, current revision = %d", event.Target.Revision, current.Revision)
	}
	receipt := Receipt{
		Version: ReceiptVersion, EventID: event.EventID, Kind: event.Kind,
		Status: "recorded", Replayed: false, Target: event.Target,
	}
	record := eventRecord{Digest: digest, Event: event, Receipt: receipt}
	encoded, _ := json.Marshal(record)
	tx, err := processor.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return Receipt{}, feedbackError(result.CodeInternal, "begin feedback record: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if existing, getErr := tx.Get(ctx, eventBucket, event.EventID); getErr == nil {
		var saved eventRecord
		if json.Unmarshal(existing, &saved) != nil || saved.Digest != digest {
			return Receipt{}, feedbackError(result.CodeRevisionConflict, "event_id %q was already used by different feedback", event.EventID)
		}
		saved.Receipt.Replayed = true
		return saved.Receipt, nil
	} else if !errors.Is(getErr, store.ErrNotFound) {
		return Receipt{}, feedbackError(result.CodeInternal, "read feedback record: %v", getErr)
	}
	if err := tx.Put(ctx, eventBucket, event.EventID, encoded); err != nil {
		return Receipt{}, feedbackError(result.CodeInternal, "write feedback record: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return Receipt{}, feedbackError(result.CodeInternal, "commit feedback record: %v", err)
	}
	return receipt, nil
}

func (processor *Processor) Confirm(ctx context.Context, confirmation Confirmation) (ConfirmationReceipt, error) {
	if err := validateConfirmation(confirmation); err != nil {
		return ConfirmationReceipt{}, err
	}
	if processor == nil || processor.store == nil || processor.reader == nil {
		return ConfirmationReceipt{}, feedbackError(result.CodeInternal, "feedback processor is not configured")
	}
	digest, err := contentDigest(confirmation)
	if err != nil {
		return ConfirmationReceipt{}, err
	}
	if receipt, found, lookupErr := processor.lookupConfirmation(ctx, confirmation.ConfirmationID, digest); lookupErr != nil || found {
		if found {
			receipt.Replayed = true
		}
		return receipt, lookupErr
	}
	event, err := processor.loadEvent(ctx, confirmation.FeedbackEventID)
	if err != nil {
		return ConfirmationReceipt{}, err
	}
	if confirmation.Action != ActionIgnore && (event.Kind == KindUseful || event.Kind == KindIrrelevant) {
		return ConfirmationReceipt{}, feedbackError(result.CodePermissionDenied, "feedback kind %q cannot authorize a fact mutation", event.Kind)
	}
	if !authorized(confirmation.AuthorizedDatabases, event.Target.Database) {
		return ConfirmationReceipt{}, feedbackError(result.CodePermissionDenied, "database %q is outside the confirmed scope", event.Target.Database)
	}
	current, err := processor.reader.Get(ctx, event.Target.Database, event.Target.Table, event.Target.RowID)
	if err != nil {
		return ConfirmationReceipt{}, normalizeError(err, "read confirmation target")
	}
	if current.Revision != confirmation.ExpectedRevision {
		return ConfirmationReceipt{}, feedbackError(result.CodeRevisionConflict, "confirmed revision = %d, current revision = %d", confirmation.ExpectedRevision, current.Revision)
	}
	if err := validateAction(confirmation, event); err != nil {
		return ConfirmationReceipt{}, err
	}
	if err := processor.reserveConfirmation(ctx, confirmation.ConfirmationID, digest); err != nil {
		return ConfirmationReceipt{}, err
	}

	receipt := ConfirmationReceipt{
		Version: ConfirmationReceiptVersion, ConfirmationID: confirmation.ConfirmationID,
		FeedbackEventID: event.EventID, Action: confirmation.Action, Target: event.Target,
		Warnings: []result.Notice{},
	}
	switch confirmation.Action {
	case ActionIgnore:
		receipt.Status, receipt.Verified = "ignored", true
	case ActionRevise:
		report, runErr := skillwrite.New(processor.tool).Run(ctx, *confirmation.Plan)
		if runErr != nil {
			return ConfirmationReceipt{}, runErr
		}
		receipt.Status = "confirmed"
		receipt.Verified = report.Receipt.Verified
		receipt.Warnings = append(receipt.Warnings, report.Receipt.Warnings...)
		if report.Receipt.Status == skillwrite.ReceiptCommittedUnverified {
			receipt.Status = "committed_unverified"
		}
		if len(report.Receipt.Changes) > 0 {
			receipt.NewRevision = cloneUint64(report.Receipt.Changes[0].Revision)
			receipt.CommitSequence = cloneUint64(report.Receipt.Changes[0].CommitSequence)
		}
	case ActionUndo:
		undoReceipt, undoErr := processor.undo(ctx, confirmation, event)
		if undoErr != nil {
			return ConfirmationReceipt{}, undoErr
		}
		receipt = undoReceipt
	}
	if err := processor.completeConfirmation(ctx, confirmation.ConfirmationID, digest, receipt); err != nil {
		return ConfirmationReceipt{}, err
	}
	return receipt, nil
}

func (processor *Processor) undo(ctx context.Context, confirmation Confirmation, event Event) (ConfirmationReceipt, error) {
	undo := *confirmation.Undo
	if processor.tool == nil {
		return ConfirmationReceipt{}, feedbackError(result.CodeInternal, "feedback mutation tool is not configured")
	}
	if _, err := processor.reader.AsOfRevision(ctx, event.Target.Database, event.Target.Table, event.Target.RowID, undo.TargetRevision); err != nil {
		return ConfirmationReceipt{}, normalizeError(err, "read undo target revision")
	}
	source := fmt.Sprintf("RESTORE %s.%s ROW :row TO REVISION :revision", event.Target.Database, event.Target.Table)
	reason := fmt.Sprintf("feedback %s: %s", event.EventID, strings.TrimSpace(confirmation.Instruction))
	call := skillwrite.Call{Phase: skillwrite.PhaseMutation, Request: executor.BatchRequest{
		RequestID: confirmation.ConfirmationID + "-undo", Source: source,
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"row": event.Target.RowID, "revision": undo.TargetRevision}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: undo.ExpectedSchemaVersion, ExpectedRevision: confirmation.ExpectedRevision,
				MaxAffectedRows: 1, Actor: confirmation.Actor, Source: confirmation.SourceEventID, Reason: reason,
				IndexTerms: undo.IndexTerms, RouteLeafIDs: undo.RouteLeafIDs,
			},
		}},
	}}
	envelope, err := processor.tool.Invoke(ctx, call)
	if err != nil {
		return ConfirmationReceipt{}, feedbackError(result.CodeInternal, "undo tool call failed: %v", err)
	}
	if err := envelope.Validate(); err != nil {
		return ConfirmationReceipt{}, feedbackError(result.CodeInternal, "undo returned an invalid envelope: %v", err)
	}
	if !envelope.OK {
		return ConfirmationReceipt{}, envelopeFailure(envelope, "undo mutation failed")
	}
	if len(envelope.Results) != 1 || envelope.Results[0].AffectedRows != 1 || envelope.Results[0].Revision == nil {
		return ConfirmationReceipt{}, feedbackError(result.CodeInternal, "undo did not report one revision")
	}
	changed := envelope.Results[0]
	current, err := processor.reader.Get(ctx, event.Target.Database, event.Target.Table, event.Target.RowID)
	verified := err == nil && current.Revision == *changed.Revision
	receipt := ConfirmationReceipt{
		Version: ConfirmationReceiptVersion, ConfirmationID: confirmation.ConfirmationID,
		FeedbackEventID: event.EventID, Action: ActionUndo, Status: "confirmed", Target: event.Target,
		NewRevision: cloneUint64(changed.Revision), CommitSequence: cloneUint64(changed.CommitSequence),
		RestoredRevision: &undo.TargetRevision,
		Verified:         verified, Warnings: []result.Notice{},
	}
	if !verified {
		receipt.Status = "committed_unverified"
		receipt.Warnings = append(receipt.Warnings, result.Notice{
			Code: result.CodeRevisionConflict, Message: "compensation committed but current revision could not be verified",
		})
	}
	return receipt, nil
}

func validateEvent(event Event) error {
	if event.Version != EventVersion {
		return feedbackError(result.CodeValidation, "event version = %q, want %q", event.Version, EventVersion)
	}
	if !bounded(event.EventID, 200) || !bounded(event.Actor, 200) || !bounded(event.Reason, 1000) ||
		!bounded(event.Target.Database, 200) || !bounded(event.Target.Table, 200) || !bounded(event.Target.RowID, 200) || event.Target.Revision == 0 {
		return feedbackError(result.CodeValidation, "bounded feedback identity, target, actor, reason, and revision are required")
	}
	switch event.Kind {
	case KindUseful, KindIrrelevant, KindStale, KindWrong, KindIncomplete:
		return nil
	default:
		return feedbackError(result.CodeValidation, "unsupported feedback kind %q", event.Kind)
	}
}

func validateConfirmation(confirmation Confirmation) error {
	if confirmation.Version != ConfirmationVersion {
		return feedbackError(result.CodeValidation, "confirmation version = %q, want %q", confirmation.Version, ConfirmationVersion)
	}
	if !bounded(confirmation.ConfirmationID, 200) || !bounded(confirmation.FeedbackEventID, 200) ||
		!bounded(confirmation.SourceEventID, 200) || !bounded(confirmation.Actor, 200) ||
		!bounded(confirmation.Instruction, 1000) || confirmation.ExpectedRevision == 0 {
		return feedbackError(result.CodeValidation, "bounded confirmation identity, provenance, instruction, and revision are required")
	}
	if confirmation.SourceEventID == confirmation.FeedbackEventID || confirmation.SourceEventID == confirmation.ConfirmationID {
		return feedbackError(result.CodeValidation, "confirmation requires a distinct user source event")
	}
	if len(confirmation.AuthorizedDatabases) == 0 || len(confirmation.AuthorizedDatabases) > 32 {
		return feedbackError(result.CodePermissionDenied, "confirmation requires a bounded database authorization scope")
	}
	for _, database := range confirmation.AuthorizedDatabases {
		if !bounded(database, 200) {
			return feedbackError(result.CodeValidation, "authorized database names must be bounded")
		}
	}
	return nil
}

func validateAction(confirmation Confirmation, event Event) error {
	switch confirmation.Action {
	case ActionIgnore:
		if confirmation.Plan != nil || confirmation.Undo != nil {
			return feedbackError(result.CodeValidation, "ignore confirmation cannot include a plan or undo")
		}
	case ActionRevise:
		if confirmation.Plan == nil || confirmation.Undo != nil {
			return feedbackError(result.CodeValidation, "revise confirmation requires only a mutation plan")
		}
		plan := confirmation.Plan
		if !strings.EqualFold(plan.Database, event.Target.Database) || !strings.EqualFold(plan.Table, event.Target.Table) ||
			plan.Actor != confirmation.Actor || plan.SourceEventID != confirmation.SourceEventID ||
			!strings.Contains(plan.Reason, event.EventID) {
			return feedbackError(result.CodeValidation, "revision plan scope or provenance does not match the confirmation")
		}
		if plan.Decision != skillwrite.DecisionRevise || len(plan.Steps) != 1 ||
			plan.Steps[0].Target != event.Target.RowID ||
			plan.Steps[0].Input.Mutation.ExpectedRevision != confirmation.ExpectedRevision {
			return feedbackError(result.CodePermissionDenied, "revision plan must revise only the confirmed Row and revision")
		}
		for _, database := range plan.AuthorizedDatabases {
			if !authorized(confirmation.AuthorizedDatabases, database) {
				return feedbackError(result.CodePermissionDenied, "revision plan expands the confirmed database scope")
			}
		}
		if err := plan.Validate(); err != nil {
			return err
		}
	case ActionUndo:
		if confirmation.Undo == nil || confirmation.Plan != nil {
			return feedbackError(result.CodeValidation, "undo confirmation requires only undo parameters")
		}
		undo := confirmation.Undo
		if undo.TargetRevision == 0 || undo.TargetRevision >= confirmation.ExpectedRevision || undo.ExpectedSchemaVersion == 0 ||
			undo.IndexTerms == nil || undo.RouteLeafIDs == nil {
			return feedbackError(result.CodeValidation, "undo requires an earlier revision, schema guard, and complete index and Route snapshots")
		}
		if len(undo.IndexTerms) > 64 || len(undo.RouteLeafIDs) > 32 {
			return feedbackError(result.CodeValidation, "undo index or Route snapshot exceeds the Policy limit")
		}
	default:
		return feedbackError(result.CodeValidation, "unsupported confirmation action %q", confirmation.Action)
	}
	return nil
}

func (processor *Processor) lookupEvent(ctx context.Context, id, digest string) (Receipt, bool, error) {
	tx, err := processor.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return Receipt{}, false, feedbackError(result.CodeInternal, "begin feedback lookup: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	encoded, err := tx.Get(ctx, eventBucket, id)
	if errors.Is(err, store.ErrNotFound) {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, feedbackError(result.CodeInternal, "read feedback: %v", err)
	}
	var record eventRecord
	if json.Unmarshal(encoded, &record) != nil || record.Digest != digest {
		return Receipt{}, false, feedbackError(result.CodeRevisionConflict, "event_id %q was already used by different feedback", id)
	}
	return record.Receipt, true, nil
}

func (processor *Processor) loadEvent(ctx context.Context, id string) (Event, error) {
	tx, err := processor.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return Event{}, feedbackError(result.CodeInternal, "begin feedback lookup: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	encoded, err := tx.Get(ctx, eventBucket, id)
	if errors.Is(err, store.ErrNotFound) {
		return Event{}, feedbackError(result.CodeNotFound, "feedback event %q was not recorded", id)
	}
	if err != nil {
		return Event{}, feedbackError(result.CodeInternal, "read feedback event: %v", err)
	}
	var record eventRecord
	if err := json.Unmarshal(encoded, &record); err != nil {
		return Event{}, feedbackError(result.CodeInternal, "decode feedback event: %v", err)
	}
	return record.Event, nil
}

func (processor *Processor) lookupConfirmation(ctx context.Context, id, digest string) (ConfirmationReceipt, bool, error) {
	tx, err := processor.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return ConfirmationReceipt{}, false, feedbackError(result.CodeInternal, "begin confirmation lookup: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	encoded, err := tx.Get(ctx, confirmationBucket, id)
	if errors.Is(err, store.ErrNotFound) {
		return ConfirmationReceipt{}, false, nil
	}
	if err != nil {
		return ConfirmationReceipt{}, false, feedbackError(result.CodeInternal, "read confirmation: %v", err)
	}
	var record confirmationRecord
	if json.Unmarshal(encoded, &record) != nil || record.Digest != digest {
		return ConfirmationReceipt{}, false, feedbackError(result.CodeRevisionConflict, "confirmation_id %q was already used by different content", id)
	}
	if record.Status != completed || record.Receipt == nil {
		return ConfirmationReceipt{}, false, feedbackError(result.CodeRevisionConflict, "confirmation %q is in doubt; inspect logical history before recovery", id)
	}
	return *record.Receipt, true, nil
}

func (processor *Processor) reserveConfirmation(ctx context.Context, id, digest string) error {
	record, _ := json.Marshal(confirmationRecord{Digest: digest, Status: processing})
	tx, err := processor.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return feedbackError(result.CodeInternal, "begin confirmation reservation: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, getErr := tx.Get(ctx, confirmationBucket, id); getErr == nil {
		return feedbackError(result.CodeRevisionConflict, "confirmation %q is already reserved", id)
	} else if !errors.Is(getErr, store.ErrNotFound) {
		return feedbackError(result.CodeInternal, "read confirmation reservation: %v", getErr)
	}
	if err := tx.Put(ctx, confirmationBucket, id, record); err != nil {
		return feedbackError(result.CodeInternal, "reserve confirmation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return feedbackError(result.CodeInternal, "commit confirmation reservation: %v", err)
	}
	return nil
}

func (processor *Processor) completeConfirmation(ctx context.Context, id, digest string, receipt ConfirmationReceipt) error {
	receipt.Replayed = false
	encodedReceipt, err := json.Marshal(receipt)
	if err != nil || len(encodedReceipt) > 4000 {
		return feedbackError(result.CodeOutputTruncated, "confirmation receipt exceeds 4000 bytes")
	}
	encoded, _ := json.Marshal(confirmationRecord{Digest: digest, Status: completed, Receipt: &receipt})
	tx, err := processor.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return feedbackError(result.CodeInternal, "begin confirmation completion: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.Put(ctx, confirmationBucket, id, encoded); err != nil {
		return feedbackError(result.CodeInternal, "complete confirmation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return feedbackError(result.CodeInternal, "commit confirmation completion: %v", err)
	}
	return nil
}

func contentDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", feedbackError(result.CodeValidation, "encode idempotent content: %v", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func bounded(value string, maximum int) bool {
	length := len(strings.TrimSpace(value))
	return length > 0 && length <= maximum
}

func authorized(databases []string, target string) bool {
	for _, database := range databases {
		if strings.EqualFold(strings.TrimSpace(database), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func normalizeError(err error, operation string) error {
	if stable, ok := err.(interface{ StableCode() string }); ok {
		code := result.Code(stable.StableCode())
		if result.IsRegisteredCode(code) {
			return feedbackError(code, "%s: %v", operation, err)
		}
	}
	return feedbackError(result.CodeInternal, "%s: %v", operation, err)
}

func envelopeFailure(envelope result.Envelope, operation string) error {
	if envelope.Error != nil {
		return feedbackError(envelope.Error.Code, "%s: %s", operation, envelope.Error.Message)
	}
	for _, statement := range envelope.Results {
		if statement.Error != nil {
			return feedbackError(statement.Error.Code, "%s: %s", operation, statement.Error.Message)
		}
	}
	return feedbackError(result.CodeInternal, "%s", operation)
}

func cloneUint64(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
