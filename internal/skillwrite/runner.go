package skillwrite

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

type Runner struct {
	tool Tool
}

func New(tool Tool) *Runner {
	return &Runner{tool: tool}
}

func (runner *Runner) Run(ctx context.Context, plan Plan) (Report, error) {
	if err := plan.Validate(); err != nil {
		return Report{}, err
	}
	if runner == nil || runner.tool == nil {
		return Report{}, mutationError(result.CodeInternal, "mutation tool is not configured")
	}
	report := Report{Calls: []Call{}}
	for _, check := range plan.Preflight {
		if err := runner.runCheck(ctx, &report, plan, PhasePreflight, check); err != nil {
			return report, err
		}
	}
	if plan.Decision == DecisionIgnore {
		report.Receipt = Receipt{
			Version: ReceiptVersion, PlanID: plan.ID, Decision: plan.Decision,
			Status: ReceiptIgnored, Changes: []Change{}, Ignored: 1,
			Verified: true, Warnings: []result.Notice{},
		}
		return report, validateReceipt(report)
	}

	mutationCall := Call{Phase: PhaseMutation, Request: mutationRequest(plan)}
	envelope, err := runner.invoke(ctx, &report, mutationCall)
	if err != nil {
		return report, err
	}
	if !envelope.OK {
		return report, envelopeError(envelope, "mutation transaction failed")
	}
	changes, err := mutationChanges(plan, envelope)
	if err != nil {
		return report, err
	}
	receipt := Receipt{
		Version: ReceiptVersion, PlanID: plan.ID, Decision: plan.Decision,
		Status: ReceiptCommitted, Changes: changes, Warnings: []result.Notice{},
		Verified: true,
	}
	for _, check := range plan.Verify {
		if err := runner.runCheck(ctx, &report, plan, PhaseVerify, check); err != nil {
			receipt.Status = ReceiptCommittedUnverified
			receipt.Verified = false
			receipt.Warnings = append(receipt.Warnings, result.Notice{
				Code: stableCode(err), Message: "mutation committed but verification did not complete",
			})
			break
		}
	}
	report.Receipt = receipt
	return report, validateReceipt(report)
}

func (runner *Runner) runCheck(
	ctx context.Context,
	report *Report,
	plan Plan,
	phase Phase,
	check Check,
) error {
	input := authorizedInput(check.Input, plan)
	call := Call{Phase: phase, Request: executor.BatchRequest{
		RequestID: plan.ID + "-" + check.ID,
		Source:    check.MSQL, Statements: []executor.StatementInput{input},
	}}
	envelope, err := runner.invoke(ctx, report, call)
	if err != nil {
		return err
	}
	if !envelope.OK {
		return envelopeError(envelope, string(phase)+" query failed")
	}
	if len(envelope.Results) != 1 {
		return mutationError(result.CodeInternal, "%s query returned %d statement results", phase, len(envelope.Results))
	}
	rows := envelope.Results[0].Rows
	if check.ExpectRows != nil && len(rows) != *check.ExpectRows {
		return mutationError(result.CodeRevisionConflict, "%s row count = %d, want %d", phase, len(rows), *check.ExpectRows)
	}
	if len(check.RowContains) > 0 {
		if len(rows) == 0 {
			return mutationError(result.CodeRevisionConflict, "%s query returned no Row", phase)
		}
		for name, want := range check.RowContains {
			if got, ok := rows[0][name]; !ok || !equivalentValue(got, want) {
				return mutationError(result.CodeRevisionConflict, "%s field %q changed", phase, name)
			}
		}
	}
	return nil
}

func equivalentValue(left, right any) bool {
	if reflect.DeepEqual(left, right) {
		return true
	}
	leftNumber, leftOK := numericString(left)
	rightNumber, rightOK := numericString(right)
	if !leftOK || !rightOK {
		return false
	}
	leftRat, leftValid := new(big.Rat).SetString(leftNumber)
	rightRat, rightValid := new(big.Rat).SetString(rightNumber)
	return leftValid && rightValid && leftRat.Cmp(rightRat) == 0
}

func numericString(value any) (string, bool) {
	switch typed := value.(type) {
	case json.Number:
		return string(typed), true
	case int:
		return fmt.Sprintf("%d", typed), true
	case int64:
		return fmt.Sprintf("%d", typed), true
	case uint64:
		return fmt.Sprintf("%d", typed), true
	case float64:
		return fmt.Sprintf("%g", typed), true
	default:
		return "", false
	}
}

func (runner *Runner) invoke(ctx context.Context, report *Report, call Call) (result.Envelope, error) {
	if err := ctx.Err(); err != nil {
		return result.Envelope{}, mutationError(result.CodeCancelled, "mutation cancelled: %v", err)
	}
	report.Calls = append(report.Calls, call)
	envelope, err := runner.tool.Invoke(ctx, call)
	if err != nil {
		return result.Envelope{}, mutationError(result.CodeInternal, "%s tool call failed: %v", call.Phase, err)
	}
	if err := envelope.Validate(); err != nil {
		return result.Envelope{}, mutationError(result.CodeInternal, "%s returned an invalid envelope: %v", call.Phase, err)
	}
	return envelope, nil
}

func mutationRequest(plan Plan) executor.BatchRequest {
	if len(plan.Steps) == 1 {
		return executor.BatchRequest{
			RequestID: plan.ID + "-mutation", Source: plan.Steps[0].MSQL,
			Statements: []executor.StatementInput{authorizedInput(plan.Steps[0].Input, plan)},
		}
	}
	sources := make([]string, 0, len(plan.Steps)+2)
	inputs := make([]executor.StatementInput, 0, len(plan.Steps)+2)
	sources = append(sources, "BEGIN")
	inputs = append(inputs, authorizedInput(executor.StatementInput{}, plan))
	for _, step := range plan.Steps {
		sources = append(sources, step.MSQL)
		inputs = append(inputs, authorizedInput(step.Input, plan))
	}
	sources = append(sources, "COMMIT")
	inputs = append(inputs, authorizedInput(executor.StatementInput{}, plan))
	return executor.BatchRequest{
		RequestID: plan.ID + "-mutation", Source: strings.Join(sources, ";"), Statements: inputs,
	}
}

func authorizedInput(input executor.StatementInput, plan Plan) executor.StatementInput {
	input.Authorization = security.Authorization{
		Version: security.AuthorizationVersion, Actor: plan.Actor,
		AuthorizedDatabases: append([]string{}, plan.AuthorizedDatabases...),
	}
	return input
}

func mutationChanges(plan Plan, envelope result.Envelope) ([]Change, error) {
	offset := 0
	if len(plan.Steps) > 1 {
		offset = 1
	}
	if len(envelope.Results) != len(plan.Steps)+2*offset {
		return nil, mutationError(result.CodeInternal, "mutation result count does not match the plan")
	}
	changes := make([]Change, len(plan.Steps))
	var commitSequence *uint64
	for index, step := range plan.Steps {
		statement := envelope.Results[index+offset]
		if statement.Status != result.StatusSucceeded || statement.AffectedRows != 1 {
			return nil, mutationError(result.CodeInternal, "mutation step %q did not report one successful change", step.ID)
		}
		if statement.CommitSequence != nil {
			if commitSequence != nil && *commitSequence != *statement.CommitSequence {
				return nil, mutationError(result.CodeInternal, "mutation steps used different commit sequences")
			}
			value := *statement.CommitSequence
			commitSequence = &value
		}
		objectID := step.Target
		if len(statement.Rows) == 1 {
			field := "row_id"
			if step.Kind == "RELATE" {
				field = "relation_id"
			}
			if value, ok := statement.Rows[0][field].(string); ok && strings.TrimSpace(value) != "" {
				objectID = value
			}
		}
		changes[index] = Change{
			Operation: step.Kind, Target: step.Target, ObjectID: objectID,
			Revision: statement.Revision, CommitSequence: statement.CommitSequence,
		}
	}
	return changes, nil
}

func validateReceipt(report Report) error {
	encoded, err := json.Marshal(report.Receipt)
	if err != nil {
		return mutationError(result.CodeInternal, "encode mutation receipt: %v", err)
	}
	if len(encoded) > 2_000 {
		return mutationError(result.CodeOutputTruncated, "mutation receipt exceeds 2000 bytes")
	}
	return nil
}

func envelopeError(envelope result.Envelope, message string) error {
	if envelope.Error != nil {
		return mutationError(envelope.Error.Code, "%s: %s", message, envelope.Error.Message)
	}
	for _, statement := range envelope.Results {
		if statement.Error != nil {
			return mutationError(statement.Error.Code, "%s: %s", message, statement.Error.Message)
		}
	}
	return mutationError(result.CodeInternal, "%s", message)
}

func stableCode(err error) result.Code {
	if stable, ok := err.(interface{ StableCode() string }); ok {
		code := result.Code(stable.StableCode())
		if result.IsRegisteredCode(code) {
			return code
		}
	}
	return result.CodeInternal
}

func mutationError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
