package skillquery

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

type Status string

const (
	StatusComplete     Status = "complete"
	StatusPartial      Status = "partial"
	StatusNoResults    Status = "no_results"
	StatusAccessDenied Status = "access_denied"
)

type Request struct {
	Question       string
	Database       string
	Table          string
	RoutePath      string
	QueryTerms     []string
	Projections    []string
	CandidateLimit int
	SelectLimit    int
}

type Call struct {
	ID        string
	Command   string
	Statement string
	MSQL      string
	Input     executor.StatementInput
}

type Tool interface {
	Invoke(context.Context, Call) (result.Envelope, error)
}

type ToolFunc func(context.Context, Call) (result.Envelope, error)

func (function ToolFunc) Invoke(ctx context.Context, call Call) (result.Envelope, error) {
	return function(ctx, call)
}

type Evidence struct {
	DatabaseID string
	TableID    string
	Database   string
	Table      string
	RowID      string
	Revision   uint64
	Values     result.Row
}

type Report struct {
	Status         Status
	Calls          []Call
	Evidence       []Evidence
	Warnings       []result.Notice
	CandidateCount int
	Truncated      bool
}

func (report Report) AnswerReady() bool {
	return len(report.Evidence) > 0 &&
		(report.Status == StatusComplete || report.Status == StatusPartial)
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string {
	return "Skill query: " + err.Message
}

func (err *Error) StableCode() string {
	return string(err.Code)
}

type Runner struct {
	tool Tool
}

func New(tool Tool) *Runner {
	return &Runner{tool: tool}
}

func (runner *Runner) Run(ctx context.Context, request Request) (Report, error) {
	if err := validateRequest(request); err != nil {
		return Report{}, err
	}
	if runner == nil || runner.tool == nil {
		return Report{}, queryError(result.CodeInternal, "query tool is not configured")
	}
	report := Report{Calls: []Call{}, Evidence: []Evidence{}, Warnings: []result.Notice{}}
	qualified := quoteIdentifier(request.Database) + "." + quoteIdentifier(request.Table)

	discovery, err := runner.call(ctx, &report, Call{
		ID: "query-01-discover", Command: "query", Statement: "SHOW", MSQL: "SHOW DATABASES",
	})
	if err != nil {
		return report, err
	}
	if stop, err := recoverFailure(&report, discovery); err != nil {
		return report, err
	} else if stop {
		return report, nil
	}
	databaseID := findIdentity(discovery, "name", request.Database, "database_id")
	if databaseID == "" {
		report.Status = StatusNoResults
		return report, nil
	}

	description, err := runner.call(ctx, &report, Call{
		ID: "query-02-describe", Command: "query", Statement: "DESCRIBE",
		MSQL: "DESCRIBE TABLE " + qualified + " COMPACT",
	})
	if err != nil {
		return report, err
	}
	if stop, err := recoverFailure(&report, description); err != nil {
		return report, err
	} else if stop {
		return report, nil
	}
	tableID := findIdentity(description, "name", request.Table, "table_id")
	if tableID == "" {
		report.Status = StatusNoResults
		return report, nil
	}

	var candidates result.Envelope
	if request.RoutePath != "" {
		candidates, err = runner.call(ctx, &report, Call{
			ID: "query-03-route", Command: "query", Statement: "OPEN_ROUTE",
			MSQL: "OPEN ROUTE FROM DATABASE :database AT :path LIMIT :limit",
			Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{
				"database": request.Database, "path": request.RoutePath, "limit": int64(request.CandidateLimit),
			}}},
		})
		if err != nil {
			return report, err
		}
		switch code := failureCode(candidates); code {
		case result.CodePermissionDenied:
			_, _ = recoverFailure(&report, candidates)
			return report, nil
		case result.CodeNotFound, result.CodeOutputTruncated:
			report.Warnings = append(report.Warnings, result.Notice{
				Code: code, Message: "Route lookup did not return usable locators; falling back to MATCH",
			})
			candidates = result.Envelope{}
		case "":
			if resultRowCount(candidates) == 0 {
				candidates = result.Envelope{}
				break
			}
			report.Truncated = candidates.Truncated
		default:
			return report, queryError(code, "Route lookup failed")
		}
	}
	if candidates.Version == "" {
		candidates, err = runner.call(ctx, &report, Call{
			ID: "query-04-match", Command: "query", Statement: "MATCH",
			MSQL: "MATCH " + qualified + " QUERY :query TERMS :terms LIMIT :limit",
			Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{
				"query": request.Question, "terms": append([]string{}, request.QueryTerms...),
				"limit": int64(request.CandidateLimit),
			}}},
		})
		if err != nil {
			return report, err
		}
		if stop, err := recoverFailure(&report, candidates); err != nil {
			return report, err
		} else if stop {
			return report, nil
		}
		report.Truncated = candidates.Truncated
	}

	locators := extractLocators(candidates, databaseID, tableID)
	report.CandidateCount = len(locators)
	if len(locators) == 0 {
		report.Status = StatusNoResults
		return report, nil
	}
	if len(locators) > request.SelectLimit {
		locators = locators[:request.SelectLimit]
		report.Truncated = true
	}
	for index, locator := range locators {
		selected, err := runner.call(ctx, &report, Call{
			ID:      fmt.Sprintf("query-%02d-select-%s", index+5, locator.rowID),
			Command: "query", Statement: "SELECT",
			MSQL: selectMSQL(qualified, request.Projections),
			Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{
				"row": locator.rowID,
			}}},
		})
		if err != nil {
			return report, err
		}
		if code := failureCode(selected); code != "" {
			if code != result.CodePermissionDenied &&
				code != result.CodeNotFound &&
				code != result.CodeOutputTruncated {
				return report, queryError(code, "SELECT failed for candidate %s", locator.rowID)
			}
			report.Warnings = append(report.Warnings, result.Notice{
				Code: code, Message: fmt.Sprintf("could not read candidate %s", locator.rowID),
			})
			continue
		}
		row, ok := firstRow(selected)
		if !ok {
			report.Warnings = append(report.Warnings, result.Notice{
				Code: result.CodeNotFound, Message: fmt.Sprintf("candidate %s no longer exists", locator.rowID),
			})
			continue
		}
		rowID, _ := row["row_id"].(string)
		revision, ok := unsigned(row["revision"])
		if rowID != locator.rowID || !ok || revision != locator.revision {
			report.Warnings = append(report.Warnings, result.Notice{
				Code:    result.CodeRevisionConflict,
				Message: fmt.Sprintf("candidate %s changed before SELECT completed", locator.rowID),
			})
			continue
		}
		report.Evidence = append(report.Evidence, Evidence{
			DatabaseID: databaseID, TableID: tableID,
			Database: request.Database, Table: request.Table,
			RowID: rowID, Revision: revision, Values: cloneRow(row),
		})
	}
	switch {
	case len(report.Evidence) == 0 && containsWarning(report.Warnings, result.CodePermissionDenied):
		report.Status = StatusAccessDenied
	case len(report.Evidence) == 0:
		report.Status = StatusNoResults
	case report.Truncated || len(report.Warnings) > 0:
		report.Status = StatusPartial
	default:
		report.Status = StatusComplete
	}
	return report, nil
}

func (runner *Runner) call(ctx context.Context, report *Report, call Call) (result.Envelope, error) {
	if err := ctx.Err(); err != nil {
		return result.Envelope{}, queryError(result.CodeCancelled, "query cancelled: %v", err)
	}
	report.Calls = append(report.Calls, call)
	envelope, err := runner.tool.Invoke(ctx, call)
	if err != nil {
		return result.Envelope{}, queryError(result.CodeInternal, "tool call %s failed: %v", call.ID, err)
	}
	if err := envelope.Validate(); err != nil {
		return result.Envelope{}, queryError(result.CodeInternal, "tool call %s returned an invalid envelope: %v", call.ID, err)
	}
	return envelope, nil
}

type locator struct {
	rowID    string
	revision uint64
}

func extractLocators(envelope result.Envelope, databaseID, tableID string) []locator {
	if len(envelope.Results) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	values := make([]locator, 0, len(envelope.Results[0].Rows))
	for _, row := range envelope.Results[0].Rows {
		rowDatabase, _ := row["database_id"].(string)
		rowTable, _ := row["table_id"].(string)
		rowID, _ := row["row_id"].(string)
		revision, ok := unsigned(row["revision"])
		if rowDatabase != databaseID || rowTable != tableID || rowID == "" || !ok || revision == 0 || seen[rowID] {
			continue
		}
		seen[rowID] = true
		values = append(values, locator{rowID: rowID, revision: revision})
	}
	return values
}

func validateRequest(request Request) error {
	if strings.TrimSpace(request.Question) == "" ||
		strings.TrimSpace(request.Database) == "" ||
		strings.TrimSpace(request.Table) == "" {
		return queryError(result.CodeValidation, "question, database, and table are required")
	}
	if request.RoutePath != "" && !strings.HasPrefix(request.RoutePath, "/") {
		return queryError(result.CodeValidation, "route path must be absolute")
	}
	if len(request.QueryTerms) == 0 || len(request.QueryTerms) > 32 {
		return queryError(result.CodeValidation, "query terms must contain 1 to 32 values")
	}
	seen := map[string]bool{}
	for _, term := range request.QueryTerms {
		normalized := strings.ToLower(strings.TrimSpace(term))
		if normalized == "" || seen[normalized] {
			return queryError(result.CodeValidation, "query terms must be non-empty and deduplicated")
		}
		seen[normalized] = true
	}
	if len(request.Projections) == 0 {
		return queryError(result.CodeValidation, "at least one projected field is required")
	}
	if len(request.Projections) > 32 {
		return queryError(result.CodeValidation, "projected fields must contain at most 32 values")
	}
	seen = map[string]bool{}
	for _, projection := range request.Projections {
		normalized := strings.ToLower(strings.TrimSpace(projection))
		if normalized == "" || seen[normalized] {
			return queryError(result.CodeValidation, "projected fields must be non-empty and deduplicated")
		}
		seen[normalized] = true
	}
	if request.CandidateLimit < 1 || request.CandidateLimit > 24 {
		return queryError(result.CodeValidation, "candidate limit must be between 1 and 24")
	}
	if request.SelectLimit < 1 || request.SelectLimit > 10 {
		return queryError(result.CodeValidation, "SELECT limit must be between 1 and 10")
	}
	return nil
}

func findIdentity(envelope result.Envelope, nameField, name, idField string) string {
	if len(envelope.Results) == 0 {
		return ""
	}
	for _, row := range envelope.Results[0].Rows {
		rowName, _ := row[nameField].(string)
		id, _ := row[idField].(string)
		if strings.EqualFold(rowName, name) && id != "" {
			return id
		}
	}
	return ""
}

func recoverFailure(report *Report, envelope result.Envelope) (bool, error) {
	code := failureCode(envelope)
	if code == "" {
		return false, nil
	}
	if code != result.CodePermissionDenied &&
		code != result.CodeNotFound &&
		code != result.CodeOutputTruncated {
		return false, queryError(code, "query step failed")
	}
	report.Warnings = append(report.Warnings, result.Notice{
		Code: code, Message: "query step did not return readable data",
	})
	if code == result.CodePermissionDenied {
		report.Status = StatusAccessDenied
	} else {
		report.Status = StatusNoResults
	}
	return true, nil
}

func failureCode(envelope result.Envelope) result.Code {
	if envelope.Error != nil {
		return envelope.Error.Code
	}
	for _, statement := range envelope.Results {
		if statement.Error != nil {
			return statement.Error.Code
		}
	}
	return ""
}

func resultRowCount(envelope result.Envelope) int {
	if len(envelope.Results) == 0 {
		return 0
	}
	return len(envelope.Results[0].Rows)
}

func firstRow(envelope result.Envelope) (result.Row, bool) {
	if len(envelope.Results) == 0 || len(envelope.Results[0].Rows) == 0 {
		return nil, false
	}
	return envelope.Results[0].Rows[0], true
}

func selectMSQL(qualified string, projections []string) string {
	fields := make([]string, 0, len(projections)+2)
	seen := map[string]bool{}
	for _, projection := range projections {
		normalized := strings.ToLower(strings.TrimSpace(projection))
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		fields = append(fields, quoteIdentifier(strings.TrimSpace(projection)))
	}
	for _, system := range []string{"row_id", "revision"} {
		if !seen[system] {
			fields = append(fields, system)
		}
	}
	return "SELECT " + strings.Join(fields, ", ") + " FROM " + qualified + " WHERE row_id = :row LIMIT 1"
}

func quoteIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func unsigned(value any) (uint64, bool) {
	switch typed := value.(type) {
	case uint64:
		return typed, true
	case int64:
		return uint64(typed), typed >= 0
	case int:
		return uint64(typed), typed >= 0
	case float64:
		return uint64(typed), typed >= 0 && typed == float64(uint64(typed))
	case json.Number:
		parsed, err := strconv.ParseUint(string(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func containsWarning(warnings []result.Notice, code result.Code) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func cloneRow(row result.Row) result.Row {
	cloned := make(result.Row, len(row))
	for name, value := range row {
		cloned[name] = value
	}
	return cloned
}

func queryError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
