package hostinput

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillwrite"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	WorthinessDecisionVersion = "memora.worthiness-decision/v1"
	WorthinessReceiptVersion  = "memora.worthiness-receipt/v1"
	decisionBucket            = "host_input.decisions.v1"
	decisionIDBucket          = "host_input.decision_ids.v1"
)

type WorthinessVerdict string

const (
	VerdictIgnore WorthinessVerdict = "IGNORE"
	VerdictWrite  WorthinessVerdict = "WRITE"
	VerdictRevise WorthinessVerdict = "REVISE"
)

type WorthinessStatus string

const WorthinessFinalized WorthinessStatus = "finalized"

type WorthinessTarget struct {
	Database string `json:"database"`
	Table    string `json:"table"`
	RowID    string `json:"row_id"`
	Revision uint64 `json:"revision"`
}

type WorthinessDecision struct {
	Version             string             `json:"version"`
	DecisionID          string             `json:"decision_id"`
	InputID             string             `json:"input_id"`
	Workspace           string             `json:"workspace"`
	Actor               string             `json:"actor"`
	InputSHA256         string             `json:"input_sha256"`
	ScopeSHA256         string             `json:"scope_sha256"`
	Verdict             WorthinessVerdict  `json:"verdict"`
	Reason              string             `json:"reason"`
	AuthorizedDatabases []string           `json:"authorized_databases"`
	Target              *WorthinessTarget  `json:"target,omitempty"`
	MutationReceipt     skillwrite.Receipt `json:"mutation_receipt"`
}

type WorthinessReceipt struct {
	Version        string            `json:"version"`
	DecisionID     string            `json:"decision_id"`
	InputID        string            `json:"input_id"`
	Workspace      string            `json:"workspace"`
	Status         WorthinessStatus  `json:"status"`
	Verdict        WorthinessVerdict `json:"verdict"`
	DecisionSHA256 string            `json:"decision_sha256"`
	InputSHA256    string            `json:"input_sha256"`
	ScopeSHA256    string            `json:"scope_sha256"`
	MutationPlanID string            `json:"mutation_plan_id"`
	DecidedAt      time.Time         `json:"decided_at"`
	Replayed       bool              `json:"replayed"`
}

type WorthinessResult struct {
	Decision WorthinessDecision `json:"decision"`
	Receipt  WorthinessReceipt  `json:"receipt"`
}

type decisionRecord struct {
	Digest   string             `json:"digest"`
	Decision WorthinessDecision `json:"decision"`
	Receipt  WorthinessReceipt  `json:"receipt"`
}

func (service *Service) Decide(ctx context.Context, decision WorthinessDecision) (WorthinessReceipt, error) {
	if service == nil || service.store == nil || service.clock == nil {
		return WorthinessReceipt{}, inputError(result.CodeInternal, "worthiness service is not configured")
	}
	decision = normalizeDecision(decision)
	if err := validateDecision(decision); err != nil {
		return WorthinessReceipt{}, err
	}
	digest, err := hashJSON(decision)
	if err != nil {
		return WorthinessReceipt{}, inputError(result.CodeInternal, "worthiness decision could not be hashed")
	}
	captureMu.Lock()
	defer captureMu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return WorthinessReceipt{}, inputError(result.CodeInternal, "begin worthiness decision: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if encoded, getErr := tx.Get(ctx, decisionBucket, decision.InputID); getErr == nil {
		current, decodeErr := decodeDecisionRecord(encoded)
		if decodeErr != nil {
			return WorthinessReceipt{}, decodeErr
		}
		if current.Digest != digest {
			return WorthinessReceipt{}, inputError(result.CodeRevisionConflict, "input_id %q already has a different decision", decision.InputID)
		}
		receipt := current.Receipt
		receipt.Replayed = true
		return receipt, nil
	} else if !errors.Is(getErr, store.ErrNotFound) {
		return WorthinessReceipt{}, inputError(result.CodeInternal, "read existing worthiness decision: %v", getErr)
	}
	if mapped, getErr := tx.Get(ctx, decisionIDBucket, decision.DecisionID); getErr == nil {
		if string(mapped) != decision.InputID {
			return WorthinessReceipt{}, inputError(result.CodeRevisionConflict, "decision_id %q was already used", decision.DecisionID)
		}
		return WorthinessReceipt{}, inputError(result.CodeInternal, "worthiness decision index is inconsistent")
	} else if !errors.Is(getErr, store.ErrNotFound) {
		return WorthinessReceipt{}, inputError(result.CodeInternal, "read worthiness decision index: %v", getErr)
	}
	encodedPending, err := tx.Get(ctx, inputBucket, decision.InputID)
	if errors.Is(err, store.ErrNotFound) {
		return WorthinessReceipt{}, inputError(result.CodeNotFound, "pending input %q was not found", decision.InputID)
	}
	if err != nil {
		return WorthinessReceipt{}, inputError(result.CodeInternal, "read pending input for decision: %v", err)
	}
	pending, err := decodeRecord(encodedPending)
	if err != nil {
		return WorthinessReceipt{}, err
	}
	if err := validateDecisionBinding(decision, pending); err != nil {
		return WorthinessReceipt{}, err
	}
	decidedAt := service.clock.Now().UTC()
	if decidedAt.IsZero() {
		return WorthinessReceipt{}, inputError(result.CodeInternal, "worthiness clock returned zero time")
	}
	receipt := WorthinessReceipt{Version: WorthinessReceiptVersion, DecisionID: decision.DecisionID,
		InputID: decision.InputID, Workspace: decision.Workspace, Status: WorthinessFinalized,
		Verdict: decision.Verdict, DecisionSHA256: digest, InputSHA256: decision.InputSHA256,
		ScopeSHA256: decision.ScopeSHA256, MutationPlanID: decision.MutationReceipt.PlanID,
		DecidedAt: decidedAt, Replayed: false}
	encoded, err := json.Marshal(decisionRecord{Digest: digest, Decision: decision, Receipt: receipt})
	if err != nil {
		return WorthinessReceipt{}, inputError(result.CodeInternal, "encode worthiness decision: %v", err)
	}
	if err := tx.Put(ctx, decisionBucket, decision.InputID, encoded); err != nil {
		return WorthinessReceipt{}, inputError(result.CodeInternal, "write worthiness decision: %v", err)
	}
	if err := tx.Put(ctx, decisionIDBucket, decision.DecisionID, []byte(decision.InputID)); err != nil {
		return WorthinessReceipt{}, inputError(result.CodeInternal, "index worthiness decision: %v", err)
	}
	if err := tx.Delete(ctx, inputBucket, decision.InputID); err != nil {
		return WorthinessReceipt{}, inputError(result.CodeInternal, "finalize pending input: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return WorthinessReceipt{}, inputError(result.CodeInternal, "commit worthiness decision: %v", err)
	}
	return receipt, nil
}

func (service *Service) GetDecision(ctx context.Context, decisionID, workspace string) (WorthinessDecision, WorthinessReceipt, error) {
	if service == nil || service.store == nil {
		return WorthinessDecision{}, WorthinessReceipt{}, inputError(result.CodeInternal, "worthiness service is not configured")
	}
	decisionID, workspace = strings.TrimSpace(decisionID), strings.TrimSpace(workspace)
	if !validText(decisionID, 200) || !validText(workspace, 500) {
		return WorthinessDecision{}, WorthinessReceipt{}, inputError(result.CodeValidation, "bounded decision ID and workspace are required")
	}
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return WorthinessDecision{}, WorthinessReceipt{}, inputError(result.CodeInternal, "begin worthiness read: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	inputID, err := tx.Get(ctx, decisionIDBucket, decisionID)
	if errors.Is(err, store.ErrNotFound) {
		return WorthinessDecision{}, WorthinessReceipt{}, inputError(result.CodeNotFound, "decision %q was not found", decisionID)
	}
	if err != nil {
		return WorthinessDecision{}, WorthinessReceipt{}, inputError(result.CodeInternal, "read worthiness index: %v", err)
	}
	encoded, err := tx.Get(ctx, decisionBucket, string(inputID))
	if err != nil {
		return WorthinessDecision{}, WorthinessReceipt{}, inputError(result.CodeInternal, "read worthiness decision: %v", err)
	}
	current, err := decodeDecisionRecord(encoded)
	if err != nil {
		return WorthinessDecision{}, WorthinessReceipt{}, err
	}
	if current.Decision.DecisionID != decisionID {
		return WorthinessDecision{}, WorthinessReceipt{}, inputError(result.CodeInternal, "worthiness decision index is inconsistent")
	}
	if current.Decision.Workspace != workspace {
		return WorthinessDecision{}, WorthinessReceipt{}, inputError(result.CodePermissionDenied, "decision workspace does not match the request")
	}
	return current.Decision, current.Receipt, nil
}

func normalizeDecision(decision WorthinessDecision) WorthinessDecision {
	decision.DecisionID, decision.InputID = strings.TrimSpace(decision.DecisionID), strings.TrimSpace(decision.InputID)
	decision.Workspace, decision.Actor, decision.Reason = strings.TrimSpace(decision.Workspace), strings.TrimSpace(decision.Actor), strings.TrimSpace(decision.Reason)
	decision.InputSHA256 = strings.ToLower(strings.TrimSpace(decision.InputSHA256))
	decision.ScopeSHA256 = strings.ToLower(strings.TrimSpace(decision.ScopeSHA256))
	decision.AuthorizedDatabases = append([]string{}, decision.AuthorizedDatabases...)
	for index := range decision.AuthorizedDatabases {
		decision.AuthorizedDatabases[index] = strings.TrimSpace(decision.AuthorizedDatabases[index])
	}
	sort.Slice(decision.AuthorizedDatabases, func(left, right int) bool {
		return strings.ToLower(decision.AuthorizedDatabases[left]) < strings.ToLower(decision.AuthorizedDatabases[right])
	})
	if decision.Target != nil {
		target := *decision.Target
		target.Database, target.Table, target.RowID = strings.TrimSpace(target.Database), strings.TrimSpace(target.Table), strings.TrimSpace(target.RowID)
		decision.Target = &target
	}
	return decision
}

func validateDecision(decision WorthinessDecision) error {
	if decision.Version != WorthinessDecisionVersion || !validText(decision.DecisionID, 200) ||
		!validText(decision.InputID, 200) || !validText(decision.Workspace, 500) ||
		!validText(decision.Actor, 160) || !validText(decision.Reason, 2_000) {
		return inputError(result.CodeValidation, "worthiness identity, workspace, actor, and reason are required and bounded")
	}
	if !validSHA256(decision.InputSHA256) || !validSHA256(decision.ScopeSHA256) {
		return inputError(result.CodeValidation, "worthiness decision requires input and scope SHA-256")
	}
	if len(decision.AuthorizedDatabases) == 0 || len(decision.AuthorizedDatabases) > 32 {
		return inputError(result.CodePermissionDenied, "worthiness decision requires 1 to 32 authorized Databases")
	}
	seen := map[string]bool{}
	for _, database := range decision.AuthorizedDatabases {
		key := strings.ToLower(database)
		if !validText(database, 200) || seen[key] {
			return inputError(result.CodeValidation, "worthiness Database selectors are invalid or duplicated")
		}
		seen[key] = true
	}
	if err := validateMutationEvidence(decision, seen); err != nil {
		return err
	}
	return nil
}

func validateMutationEvidence(decision WorthinessDecision, authorized map[string]bool) error {
	receipt := decision.MutationReceipt
	if receipt.Version != skillwrite.ReceiptVersion || !validText(receipt.PlanID, 200) {
		return inputError(result.CodeValidation, "worthiness decision requires a valid Mutation Receipt")
	}
	switch decision.Verdict {
	case VerdictIgnore:
		if decision.Target != nil || receipt.Decision != skillwrite.DecisionIgnore ||
			receipt.Status != skillwrite.ReceiptIgnored || !receipt.Verified || receipt.Ignored != 1 || len(receipt.Changes) != 0 {
			return inputError(result.CodeValidation, "IGNORE requires one verified ignored Mutation Receipt and no target")
		}
		return nil
	case VerdictWrite, VerdictRevise:
		wantDecision, wantOperation := skillwrite.DecisionInsert, "INSERT"
		if decision.Verdict == VerdictRevise {
			wantDecision, wantOperation = skillwrite.DecisionRevise, "UPDATE"
		}
		if receipt.Decision != wantDecision || receipt.Status != skillwrite.ReceiptCommitted ||
			!receipt.Verified || receipt.Ignored != 0 || decision.Target == nil {
			return inputError(result.CodeValidation, "%s requires one committed and verified %s Mutation Receipt", decision.Verdict, wantDecision)
		}
		target := decision.Target
		if !validText(target.Database, 200) || !validText(target.Table, 200) || !validText(target.RowID, 200) ||
			target.Revision == 0 || !authorized[strings.ToLower(target.Database)] {
			return inputError(result.CodePermissionDenied, "worthiness target must be bounded and authorized")
		}
		matches := 0
		for _, change := range receipt.Changes {
			if change.Operation == wantOperation && change.ObjectID == target.RowID &&
				change.Revision != nil && *change.Revision == target.Revision {
				matches++
			}
		}
		if matches != 1 {
			return inputError(result.CodeValidation, "worthiness target does not match exactly one Mutation Receipt change")
		}
		return nil
	default:
		return inputError(result.CodeValidation, "worthiness verdict must be IGNORE, WRITE, or REVISE")
	}
}

func validateDecisionBinding(decision WorthinessDecision, pending record) error {
	if decision.InputID != pending.Candidate.InputID || decision.Workspace != pending.Candidate.Workspace ||
		decision.Actor != pending.Candidate.Actor || decision.InputSHA256 != pending.Receipt.InputSHA256 ||
		decision.ScopeSHA256 != pending.Receipt.ScopeSHA256 ||
		!sameStrings(decision.AuthorizedDatabases, pending.Candidate.AuthorizedDatabases) {
		return inputError(result.CodeRevisionConflict, "worthiness decision does not match the pending capture receipt")
	}
	return nil
}

func decodeDecisionRecord(encoded []byte) (decisionRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value decisionRecord
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF || validateDecision(value.Decision) != nil {
		return decisionRecord{}, inputError(result.CodeInternal, "stored worthiness decision is invalid")
	}
	want, err := hashJSON(value.Decision)
	if err != nil || value.Digest != want || value.Receipt.Version != WorthinessReceiptVersion ||
		value.Receipt.Status != WorthinessFinalized || value.Receipt.Replayed || value.Receipt.DecidedAt.IsZero() ||
		value.Receipt.DecisionSHA256 != value.Digest || value.Receipt.DecisionID != value.Decision.DecisionID ||
		value.Receipt.InputID != value.Decision.InputID || value.Receipt.Workspace != value.Decision.Workspace ||
		value.Receipt.Verdict != value.Decision.Verdict || value.Receipt.InputSHA256 != value.Decision.InputSHA256 ||
		value.Receipt.ScopeSHA256 != value.Decision.ScopeSHA256 ||
		value.Receipt.MutationPlanID != value.Decision.MutationReceipt.PlanID {
		return decisionRecord{}, inputError(result.CodeInternal, "stored worthiness decision integrity check failed")
	}
	return value, nil
}
