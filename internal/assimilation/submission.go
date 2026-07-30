package assimilation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillwrite"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	submissionBucket          = "assimilation.submissions.v1"
	sourceReceiptBucket       = "assimilation.source-receipts.v1"
	maximumModules            = 64
	maximumRelationships      = 128
	maximumKeyFacts           = 256
	maximumSubmissionBytes    = 512 * 1024
	maximumSourceReceiptBytes = 64 * 1024
)

type submissionRecordStatus string

const (
	submissionProcessing submissionRecordStatus = "processing"
	submissionCompleted  submissionRecordStatus = "completed"
)

type submissionRecord struct {
	Digest  string                 `json:"digest"`
	Status  submissionRecordStatus `json:"status"`
	Receipt *SourceReceipt         `json:"receipt,omitempty"`
}

type coverageSnapshot struct {
	Revision        uint64
	Source          Source
	Units           map[string]Unit
	ReviewChallenge string
}

func (processor *Processor) Submit(
	ctx context.Context,
	submission Submission,
	tool skillwrite.Tool,
) (SourceReceipt, error) {
	if processor == nil || processor.store == nil {
		return SourceReceipt{}, submissionError(result.CodeInternal, "processor is not configured")
	}
	if tool == nil {
		return SourceReceipt{}, submissionError(result.CodeInternal, "mutation tool is not configured")
	}
	encoded, err := json.Marshal(submission)
	if err != nil {
		return SourceReceipt{}, submissionError(result.CodeValidation, "encode submission: %v", err)
	}
	if len(encoded) > maximumSubmissionBytes {
		return SourceReceipt{}, submissionError(result.CodeOutputTruncated, "submission exceeds %d bytes", maximumSubmissionBytes)
	}
	digest := sha256Digest(encoded)
	if existing, found, err := processor.lookupSubmission(ctx, submission.SubmissionID, digest); err != nil {
		return SourceReceipt{}, err
	} else if found {
		existing.Replayed = true
		return existing, nil
	}

	snapshot, err := processor.coverageSnapshot(ctx, submission.TaskID, submission.Workspace)
	if err != nil {
		return SourceReceipt{}, err
	}
	if err := validateSubmission(submission, snapshot); err != nil {
		return SourceReceipt{}, err
	}
	base := sourceReceiptBase(submission, snapshot.Source)
	if len(submission.UnresolvedConflictIDs) > 0 {
		base.Status = SubmissionNeedsUser
		base.UnresolvedConflictIDs = append([]string{}, submission.UnresolvedConflictIDs...)
		if err := processor.reserveSubmission(ctx, submission.SubmissionID, digest); err != nil {
			return SourceReceipt{}, err
		}
		if err := processor.completeSubmission(ctx, submission.SubmissionID, digest, base); err != nil {
			return SourceReceipt{}, err
		}
		return base, nil
	}
	if err := processor.reserveSubmission(ctx, submission.SubmissionID, digest); err != nil {
		return SourceReceipt{}, err
	}

	runner := skillwrite.New(tool)
	moduleObjects := make(map[string]string, len(submission.Modules))
	for _, module := range submission.Modules {
		plan := withReviewedSource(module.Plan, submission.SubmissionID, snapshot.Source)
		report, runErr := runner.Run(ctx, plan)
		if runErr != nil {
			return processor.completeInDoubt(ctx, submission.SubmissionID, digest, base, runErr)
		}
		base.Impacts = append(base.Impacts, impact("module", module.ID, report.Receipt))
		base.Warnings = append(base.Warnings, report.Receipt.Warnings...)
		if !report.Receipt.Verified {
			return processor.completeInDoubt(ctx, submission.SubmissionID, digest, base,
				fmt.Errorf("module plan %q committed without verification", module.Plan.ID))
		}
		if len(report.Receipt.Changes) == 1 && report.Receipt.Changes[0].ObjectID != "" {
			moduleObjects[module.ID] = report.Receipt.Changes[0].ObjectID
		}
	}
	for _, relationship := range submission.Relationships {
		plan, bindErr := bindRelationshipPlan(relationship, moduleObjects)
		if bindErr != nil {
			return processor.completeInDoubt(ctx, submission.SubmissionID, digest, base, bindErr)
		}
		plan = withReviewedSource(plan, submission.SubmissionID, snapshot.Source)
		report, runErr := runner.Run(ctx, plan)
		if runErr != nil {
			return processor.completeInDoubt(ctx, submission.SubmissionID, digest, base, runErr)
		}
		base.Impacts = append(base.Impacts, impact("relationship", relationship.ID, report.Receipt))
		base.Warnings = append(base.Warnings, report.Receipt.Warnings...)
		if !report.Receipt.Verified {
			return processor.completeInDoubt(ctx, submission.SubmissionID, digest, base,
				fmt.Errorf("relationship plan %q committed without verification", relationship.Plan.ID))
		}
	}
	base.Status = SubmissionCommitted
	for _, fact := range submission.KeyFacts {
		base.KeyFacts = append(base.KeyFacts, KeyFactReceipt{
			ID: fact.ID, ModuleID: fact.ModuleID, Field: fact.Field,
			ValueDigest: fact.ValueDigest, Anchor: fact.Anchor,
		})
	}
	if err := processor.completeSubmission(ctx, submission.SubmissionID, digest, base); err != nil {
		return SourceReceipt{}, err
	}
	return base, nil
}

func withReviewedSource(plan skillwrite.Plan, receiptID string, source Source) skillwrite.Plan {
	plan.Steps = append([]skillwrite.Step{}, plan.Steps...)
	for index := range plan.Steps {
		plan.Steps[index].Input.Mutation.SourceKind = history.SourceReviewed
		plan.Steps[index].Input.Mutation.SourceReceiptID = receiptID
		plan.Steps[index].Input.Mutation.SourceLocator = source.Locator
		plan.Steps[index].Input.Mutation.SourceContentHash = source.ContentHash
	}
	return plan
}

func (processor *Processor) SourceReceipt(ctx context.Context, submissionID string) (SourceReceipt, error) {
	if processor == nil || processor.store == nil || !validShort(submissionID, 200) {
		return SourceReceipt{}, submissionError(result.CodeValidation, "bounded submission ID is required")
	}
	tx, err := processor.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return SourceReceipt{}, submissionError(result.CodeInternal, "begin Source Receipt read: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	encoded, err := tx.Get(ctx, sourceReceiptBucket, submissionID)
	if errors.Is(err, store.ErrNotFound) {
		recordBytes, recordErr := tx.Get(ctx, submissionBucket, submissionID)
		if errors.Is(recordErr, store.ErrNotFound) {
			return SourceReceipt{}, submissionError(result.CodeNotFound, "Source Receipt %q was not found", submissionID)
		}
		if recordErr != nil {
			return SourceReceipt{}, submissionError(result.CodeInternal, "read submission state: %v", recordErr)
		}
		var record submissionRecord
		if json.Unmarshal(recordBytes, &record) != nil || record.Status != submissionProcessing {
			return SourceReceipt{}, submissionError(result.CodeInternal, "submission state is invalid")
		}
		return SourceReceipt{
			Version: SourceReceiptVersion, SubmissionID: submissionID, Status: SubmissionInDoubt,
			Impacts: []SourceImpact{}, KeyFacts: []KeyFactReceipt{}, Warnings: []result.Notice{{
				Code: result.CodeRevisionConflict, Message: "submission was interrupted after write dispatch; inspect logical Rows before recovery",
			}},
		}, nil
	}
	if err != nil {
		return SourceReceipt{}, submissionError(result.CodeInternal, "read Source Receipt: %v", err)
	}
	var receipt SourceReceipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		return SourceReceipt{}, submissionError(result.CodeInternal, "decode Source Receipt: %v", err)
	}
	return receipt, nil
}

func validateSubmission(submission Submission, snapshot coverageSnapshot) error {
	if submission.Version != SubmissionVersion {
		return submissionError(result.CodeValidation, "submission version = %q, want %q", submission.Version, SubmissionVersion)
	}
	if !validShort(submission.SubmissionID, 200) || !validShort(submission.TaskID, 200) ||
		!validShort(submission.Workspace, 500) || !validShort(submission.Author, 200) ||
		!validShort(submission.DraftContextID, 200) || !validDigest(submission.DraftDigest) {
		return submissionError(result.CodeValidation, "submission identity, scope, author, context, and draft digest are required and bounded")
	}
	if submission.CoverageRevision == 0 || submission.CoverageRevision != snapshot.Revision {
		return submissionError(result.CodeRevisionConflict, "coverage revision = %d, current = %d", submission.CoverageRevision, snapshot.Revision)
	}
	if len(submission.Modules) == 0 || len(submission.Modules) > maximumModules ||
		len(submission.Relationships) > maximumRelationships || len(submission.KeyFacts) > maximumKeyFacts {
		return submissionError(result.CodeValidation, "submission module, relationship, or key-fact count exceeds policy")
	}
	moduleIDs, relationshipIDs, factIDs := []string{}, []string{}, []string{}
	seenIDs, seenPlans := map[string]bool{}, map[string]bool{}
	for index, module := range submission.Modules {
		if err := validateSubmissionItem("module", index, module.ID, module.Anchors, module.Plan, submission, snapshot, seenIDs, seenPlans); err != nil {
			return err
		}
		if module.Plan.Decision == skillwrite.DecisionRelate {
			return submissionError(result.CodeValidation, "module %q cannot use RELATE", module.ID)
		}
		moduleIDs = append(moduleIDs, module.ID)
	}
	for index, relationship := range submission.Relationships {
		if err := validateSubmissionItem("relationship", index, relationship.ID, relationship.Anchors, relationship.Plan, submission, snapshot, seenIDs, seenPlans); err != nil {
			return err
		}
		if relationship.Plan.Decision != skillwrite.DecisionRelate {
			return submissionError(result.CodeValidation, "relationship %q requires RELATE", relationship.ID)
		}
		relationshipIDs = append(relationshipIDs, relationship.ID)
	}
	moduleSet := stringSet(moduleIDs)
	moduleDecisions := make(map[string]skillwrite.Decision, len(submission.Modules))
	for _, module := range submission.Modules {
		moduleDecisions[module.ID] = module.Plan.Decision
	}
	for _, relationship := range submission.Relationships {
		if !moduleSet[relationship.SourceModuleID] || !moduleSet[relationship.TargetModuleID] {
			return submissionError(result.CodeValidation, "relationship %q must reference submitted source and target modules", relationship.ID)
		}
		for _, moduleID := range []string{relationship.SourceModuleID, relationship.TargetModuleID} {
			decision := moduleDecisions[moduleID]
			if decision != skillwrite.DecisionInsert && decision != skillwrite.DecisionRevise && decision != skillwrite.DecisionMove {
				return submissionError(result.CodeValidation,
					"relationship %q endpoint module %q must produce one logical object", relationship.ID, moduleID)
			}
		}
		if err := validateRelationshipPlanBindings(relationship); err != nil {
			return err
		}
	}
	for index, fact := range submission.KeyFacts {
		if !validShort(fact.ID, 200) || seenIDs[fact.ID] || !moduleSet[fact.ModuleID] ||
			!validShort(fact.Field, 200) || !validDigest(fact.ValueDigest) {
			return submissionError(result.CodeValidation, "key fact %d identity, module, field, or digest is invalid", index)
		}
		seenIDs[fact.ID] = true
		if err := validateAnchor(fact.Anchor, snapshot); err != nil {
			return submissionError(result.CodeValidation, "key fact %q anchor: %v", fact.ID, err)
		}
		factIDs = append(factIDs, fact.ID)
	}
	if err := validateConflictIDs(submission.UnresolvedConflictIDs); err != nil {
		return err
	}
	return validateReview(submission, moduleIDs, relationshipIDs, factIDs, snapshot.ReviewChallenge)
}

func validateRelationshipPlanBindings(relationship SemanticRelationship) error {
	if len(relationship.Plan.Steps) != 1 {
		return submissionError(result.CodeValidation, "relationship %q requires one RELATE step", relationship.ID)
	}
	named := relationship.Plan.Steps[0].Input.Parameters.Named
	if named == nil || named["source"] != relationship.SourceModuleID || named["target"] != relationship.TargetModuleID {
		return submissionError(result.CodeValidation,
			"relationship %q must bind :source and :target to its reviewed module IDs", relationship.ID)
	}
	return nil
}

func bindRelationshipPlan(
	relationship SemanticRelationship,
	moduleObjects map[string]string,
) (skillwrite.Plan, error) {
	sourceID, sourceFound := moduleObjects[relationship.SourceModuleID]
	targetID, targetFound := moduleObjects[relationship.TargetModuleID]
	if !sourceFound || !targetFound {
		return skillwrite.Plan{}, submissionError(result.CodeRevisionConflict,
			"relationship %q endpoint module did not produce one verified logical object", relationship.ID)
	}
	plan := relationship.Plan
	plan.Preflight = cloneChecksWithModuleBindings(relationship.Plan.Preflight, relationship, sourceID, targetID)
	plan.Steps = append([]skillwrite.Step{}, relationship.Plan.Steps...)
	for index := range plan.Steps {
		plan.Steps[index].Input.Parameters.Named = bindModuleParameters(
			relationship.Plan.Steps[index].Input.Parameters.Named, relationship, sourceID, targetID,
		)
	}
	plan.Steps[0].Target = sourceID + "->" + targetID
	plan.Verify = cloneChecksWithModuleBindings(relationship.Plan.Verify, relationship, sourceID, targetID)
	return plan, nil
}

func cloneChecksWithModuleBindings(
	checks []skillwrite.Check,
	relationship SemanticRelationship,
	sourceID, targetID string,
) []skillwrite.Check {
	cloned := append([]skillwrite.Check{}, checks...)
	for index := range cloned {
		cloned[index].Input.Parameters.Named = bindModuleParameters(
			checks[index].Input.Parameters.Named, relationship, sourceID, targetID,
		)
	}
	return cloned
}

func bindModuleParameters(
	values map[string]any,
	relationship SemanticRelationship,
	sourceID, targetID string,
) map[string]any {
	if values == nil {
		return nil
	}
	bound := make(map[string]any, len(values))
	for name, value := range values {
		text, isText := value.(string)
		switch {
		case isText && text == relationship.SourceModuleID:
			bound[name] = sourceID
		case isText && text == relationship.TargetModuleID:
			bound[name] = targetID
		default:
			bound[name] = value
		}
	}
	return bound
}

func validateSubmissionItem(
	kind string,
	index int,
	id string,
	anchors []SourceAnchor,
	plan skillwrite.Plan,
	submission Submission,
	snapshot coverageSnapshot,
	seenIDs, seenPlans map[string]bool,
) error {
	if !validShort(id, 200) || seenIDs[id] || len(anchors) == 0 || len(anchors) > 32 {
		return submissionError(result.CodeValidation, "%s %d identity or anchor count is invalid", kind, index)
	}
	seenIDs[id] = true
	for anchorIndex, anchor := range anchors {
		if err := validateAnchor(anchor, snapshot); err != nil {
			return submissionError(result.CodeValidation, "%s %q anchor %d: %v", kind, id, anchorIndex, err)
		}
	}
	if seenPlans[plan.ID] {
		return submissionError(result.CodeValidation, "plan ID %q is duplicated", plan.ID)
	}
	seenPlans[plan.ID] = true
	if plan.Actor != submission.Author || plan.SourceEventID != submission.SubmissionID {
		return submissionError(result.CodeValidation, "%s %q plan provenance does not match submission", kind, id)
	}
	if err := plan.Validate(); err != nil {
		return submissionError(result.CodeValidation, "%s %q Mutation Plan: %v", kind, id, err)
	}
	return nil
}

func validateAnchor(anchor SourceAnchor, snapshot coverageSnapshot) error {
	unit, found := snapshot.Units[anchor.UnitID]
	if !found || unit.Extent == 0 || anchor.Start >= anchor.End || anchor.End > unit.Extent {
		return fmt.Errorf("range or readable unit is invalid")
	}
	return nil
}

func validateReview(submission Submission, modules, relationships, facts []string, challenge string) error {
	review := submission.Review
	if review.Version != ReviewVersion || !validShort(review.Reviewer, 200) || !validShort(review.ContextID, 200) ||
		review.Reviewer == submission.Author || review.ContextID == submission.DraftContextID ||
		review.DraftDigest != submission.DraftDigest ||
		review.CoverageRevision != submission.CoverageRevision || review.Verdict != ReviewAccepted {
		return submissionError(result.CodeValidation, "review identity, isolated context, draft, coverage revision, or verdict is invalid")
	}
	if !validDigest(challenge) || review.Challenge != challenge || !validDigest(review.FindingsDigest) ||
		review.ArtifactDigest != ReviewArtifactDigest(review) {
		return submissionError(result.CodeValidation, "review is not bound to the engine challenge and complete review artifact")
	}
	if !review.AnchorsVerified || !review.KeyFactsVerified || !review.ConflictsChecked || !review.RawContentAbsent {
		return submissionError(result.CodeValidation, "review must verify anchors, key facts, conflicts, and raw-content absence")
	}
	if !sameStringSet(review.CheckedModuleIDs, modules) ||
		!sameStringSet(review.CheckedRelationshipIDs, relationships) ||
		!sameStringSet(review.CheckedKeyFactIDs, facts) {
		return submissionError(result.CodeValidation, "review must cover the exact module, relationship, and key-fact ID sets")
	}
	return nil
}

func validateConflictIDs(values []string) error {
	if len(values) > 32 {
		return submissionError(result.CodeValidation, "unresolved conflict count exceeds 32")
	}
	seen := map[string]bool{}
	for _, value := range values {
		if !validShort(value, 200) || seen[value] {
			return submissionError(result.CodeValidation, "unresolved conflict IDs must be bounded and deduplicated")
		}
		seen[value] = true
	}
	return nil
}

func (processor *Processor) coverageSnapshot(ctx context.Context, taskID, workspace string) (coverageSnapshot, error) {
	if !validShort(taskID, 200) || !validShort(workspace, 500) {
		return coverageSnapshot{}, submissionError(result.CodeValidation, "task and workspace are required")
	}
	tx, err := processor.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return coverageSnapshot{}, submissionError(result.CodeInternal, "begin coverage read: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	state, err := loadTask(ctx, tx, taskID)
	if err != nil {
		return coverageSnapshot{}, err
	}
	if state.Workspace != workspace {
		return coverageSnapshot{}, submissionError(result.CodePermissionDenied, "task workspace does not match submission")
	}
	if !state.Complete || len(allUnread(state)) != 0 {
		return coverageSnapshot{}, submissionError(result.CodeRevisionConflict, "coverage is not complete")
	}
	units := make(map[string]Unit, len(state.Inventory.Units))
	for _, unit := range state.Inventory.Units {
		units[unit.ID] = unit
	}
	return coverageSnapshot{
		Revision: state.Revision, Source: state.Inventory.Source, Units: units,
		ReviewChallenge: state.ReviewChallenge,
	}, nil
}

func (processor *Processor) lookupSubmission(ctx context.Context, id, digest string) (SourceReceipt, bool, error) {
	if !validShort(id, 200) {
		return SourceReceipt{}, false, submissionError(result.CodeValidation, "bounded submission ID is required")
	}
	tx, err := processor.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return SourceReceipt{}, false, submissionError(result.CodeInternal, "begin submission lookup: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	encoded, err := tx.Get(ctx, submissionBucket, id)
	if errors.Is(err, store.ErrNotFound) {
		return SourceReceipt{}, false, nil
	}
	if err != nil {
		return SourceReceipt{}, false, submissionError(result.CodeInternal, "read submission: %v", err)
	}
	var record submissionRecord
	if err := json.Unmarshal(encoded, &record); err != nil {
		return SourceReceipt{}, false, submissionError(result.CodeInternal, "decode submission: %v", err)
	}
	if record.Digest != digest {
		return SourceReceipt{}, false, submissionError(result.CodeRevisionConflict, "submission_id %q was already used by different content", id)
	}
	if record.Status == submissionProcessing || record.Receipt == nil {
		return SourceReceipt{
			Version: SourceReceiptVersion, SubmissionID: id, Status: SubmissionInDoubt,
			Impacts: []SourceImpact{}, KeyFacts: []KeyFactReceipt{}, Warnings: []result.Notice{{
				Code: result.CodeRevisionConflict, Message: "submission is in doubt; inspect logical Rows before recovery",
			}},
		}, true, nil
	}
	return *record.Receipt, true, nil
}

func (processor *Processor) reserveSubmission(ctx context.Context, id, digest string) error {
	record := submissionRecord{Digest: digest, Status: submissionProcessing}
	encoded, _ := json.Marshal(record)
	tx, err := processor.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return submissionError(result.CodeInternal, "begin submission reservation: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if existing, getErr := tx.Get(ctx, submissionBucket, id); getErr == nil {
		var current submissionRecord
		if json.Unmarshal(existing, &current) != nil || current.Digest != digest {
			return submissionError(result.CodeRevisionConflict, "submission ID is already reserved")
		}
		return submissionError(result.CodeRevisionConflict, "submission is already processing")
	} else if !errors.Is(getErr, store.ErrNotFound) {
		return submissionError(result.CodeInternal, "read submission reservation: %v", getErr)
	}
	if err := tx.Put(ctx, submissionBucket, id, encoded); err != nil {
		return submissionError(result.CodeInternal, "reserve submission: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return submissionError(result.CodeInternal, "commit submission reservation: %v", err)
	}
	return nil
}

func (processor *Processor) completeInDoubt(
	ctx context.Context,
	id, digest string,
	receipt SourceReceipt,
	cause error,
) (SourceReceipt, error) {
	receipt.Status = SubmissionInDoubt
	receipt.Warnings = append(receipt.Warnings, result.Notice{
		Code: stableSubmissionCode(cause), Message: "semantic submission may be partially committed; inspect logical Rows before recovery",
	})
	if err := processor.completeSubmission(ctx, id, digest, receipt); err != nil {
		return SourceReceipt{}, err
	}
	return receipt, nil
}

func (processor *Processor) completeSubmission(ctx context.Context, id, digest string, receipt SourceReceipt) error {
	receipt.Replayed = false
	receiptBytes, err := json.Marshal(receipt)
	if err != nil || len(receiptBytes) > maximumSourceReceiptBytes {
		return submissionError(result.CodeOutputTruncated, "Source Receipt exceeds %d bytes", maximumSourceReceiptBytes)
	}
	record := submissionRecord{Digest: digest, Status: submissionCompleted, Receipt: &receipt}
	recordBytes, _ := json.Marshal(record)
	tx, err := processor.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return submissionError(result.CodeInternal, "begin Source Receipt commit: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.Put(ctx, submissionBucket, id, recordBytes); err != nil {
		return submissionError(result.CodeInternal, "complete submission: %v", err)
	}
	if err := tx.Put(ctx, sourceReceiptBucket, id, receiptBytes); err != nil {
		return submissionError(result.CodeInternal, "write Source Receipt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return submissionError(result.CodeInternal, "commit Source Receipt: %v", err)
	}
	return nil
}

func sourceReceiptBase(submission Submission, source Source) SourceReceipt {
	return SourceReceipt{
		Version: SourceReceiptVersion, SubmissionID: submission.SubmissionID, TaskID: submission.TaskID,
		Workspace: submission.Workspace, Source: source, CoverageRevision: submission.CoverageRevision,
		Author: submission.Author, Reviewer: submission.Review.Reviewer,
		SourceStrength:       history.SourceReviewed,
		ReviewArtifactDigest: submission.Review.ArtifactDigest,
		Impacts:              []SourceImpact{}, KeyFacts: []KeyFactReceipt{}, Warnings: []result.Notice{},
	}
}

func impact(kind, id string, receipt skillwrite.Receipt) SourceImpact {
	return SourceImpact{
		Kind: kind, ID: id, PlanID: receipt.PlanID, Decision: receipt.Decision,
		Status: receipt.Status, Changes: append([]skillwrite.Change{}, receipt.Changes...),
	}
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	lcopy, rcopy := append([]string{}, left...), append([]string{}, right...)
	sort.Strings(lcopy)
	sort.Strings(rcopy)
	for index := range lcopy {
		if lcopy[index] == "" || (index > 0 && lcopy[index] == lcopy[index-1]) || lcopy[index] != rcopy[index] {
			return false
		}
	}
	return true
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func sha256Digest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func stableSubmissionCode(err error) result.Code {
	if stable, ok := err.(interface{ StableCode() string }); ok {
		code := result.Code(stable.StableCode())
		if result.IsRegisteredCode(code) {
			return code
		}
	}
	if strings.Contains(strings.ToLower(err.Error()), "cancel") {
		return result.CodeCancelled
	}
	return result.CodeInternal
}
