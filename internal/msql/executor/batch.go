package executor

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/lexer"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
)

type StatementInput struct {
	Parameters    Parameters             `json:"parameters,omitempty"`
	Mutation      MutationOptions        `json:"mutation,omitempty"`
	Authorization security.Authorization `json:"authorization,omitempty"`
}

type BatchRequest struct {
	RequestID  string           `json:"request_id"`
	Source     string           `json:"source"`
	Statements []StatementInput `json:"statements,omitempty"`
}

// BatchSession serializes requests for one connection and owns any explicit
// Row transaction that spans request boundaries.
type BatchSession struct {
	mu         sync.Mutex
	context    context.Context
	cancel     context.CancelFunc
	autocommit *Engine
	rows       Rows
	points     PointReads
	active     *row.Transaction
	aborted    bool
	closed     bool
}

func NewBatchSession(ctx context.Context, dictionary Catalog, rows Rows) *BatchSession {
	return NewBatchSessionWithPackages(ctx, dictionary, rows, nil)
}

func NewBatchSessionWithPointReads(
	ctx context.Context,
	dictionary Catalog,
	rows Rows,
	points PointReads,
) *BatchSession {
	if ctx == nil {
		ctx = context.Background()
	}
	sessionContext, cancel := context.WithCancel(ctx)
	return &BatchSession{
		context: sessionContext, cancel: cancel,
		autocommit: NewWithPointReads(dictionary, rows, points), rows: rows, points: points,
	}
}

func NewBatchSessionWithPackages(
	ctx context.Context,
	dictionary Catalog,
	rows Rows,
	packages PackageManager,
) *BatchSession {
	return NewBatchSessionWithManagement(ctx, dictionary, rows, packages, nil)
}

func NewBatchSessionWithManagement(
	ctx context.Context,
	dictionary Catalog,
	rows Rows,
	packages PackageManager,
	wiki WikiExporter,
) *BatchSession {
	if ctx == nil {
		ctx = context.Background()
	}
	sessionContext, cancel := context.WithCancel(ctx)
	return &BatchSession{
		context: sessionContext, cancel: cancel,
		autocommit: NewWithManagement(dictionary, rows, packages, wiki), rows: rows,
	}
}

func (session *BatchSession) Execute(ctx context.Context, request BatchRequest) result.Envelope {
	session.mu.Lock()
	defer session.mu.Unlock()

	if request.RequestID == "" {
		return result.FailedRequest("unknown", result.CodeInvalidRequest, "request_id is empty", false)
	}
	if session.closed {
		return result.FailedRequest(request.RequestID, result.CodeInvalidRequest, "MSQL session is closed", false)
	}
	items, err := parser.ParseBatchItems(request.Source)
	if err != nil {
		return result.FailedRequest(request.RequestID, result.CodeParseError, parseMessage(err), false)
	}
	inputs, ok := statementInputs(request.Statements, len(items))
	if !ok {
		return result.FailedRequest(
			request.RequestID,
			result.CodeInvalidRequest,
			"statement input count must be zero or equal the parsed statement count",
			false,
		)
	}

	results := make([]result.StatementResult, 0, len(items))
	transactionStart := -1
	if session.active != nil || session.aborted {
		transactionStart = 0
	}
	for index, item := range items {
		source := spanSource(request.Source, item.Span, item.Kind)
		if session.aborted {
			results = append(results, skippedStatement(index, item.Kind, source))
			if item.Kind == "COMMIT" || item.Kind == "ROLLBACK" {
				session.aborted = false
				transactionStart = -1
			}
			continue
		}
		if item.Issue != nil {
			parseFailure := failedStatement(
				index, item.Kind, source, parserResultCode(item.Issue), item.Issue.Error(),
			)
			if session.active != nil && mutationKind(item.Kind) {
				rollbackErr := session.active.Rollback()
				session.active = nil
				markRolledBack(results, transactionStart, "transaction was rolled back after a write parse failure")
				if rollbackErr != nil {
					parseFailure = statementFailure(index, item.Kind, source, rollbackErr)
				}
				session.aborted = true
			}
			results = append(results, parseFailure)
			continue
		}
		statement := *item.Statement

		switch statement.Kind {
		case "BEGIN":
			if session.active != nil {
				results = append(results, failedStatement(
					index, statement.Kind, source, result.CodeInvalidTransaction,
					"cannot BEGIN while transaction is active",
				))
				continue
			}
			if session.rows == nil {
				results = append(results, failedStatement(
					index, statement.Kind, source, result.CodeInternal,
					"transaction engine is not configured",
				))
				continue
			}
			transactional, ok := session.rows.(interface {
				BeginTransaction(context.Context) (*row.Transaction, error)
			})
			if !ok {
				results = append(results, failedStatement(
					index, statement.Kind, source, result.CodeUnsupported,
					"explicit transactions are not supported by this backend",
				))
				continue
			}
			transaction, beginErr := transactional.BeginTransaction(session.context)
			if beginErr != nil {
				results = append(results, statementFailure(index, statement.Kind, source, beginErr))
				continue
			}
			session.active = transaction
			transactionStart = index
			results = append(results, result.NewStatement(index, statement.Kind, source))
		case "COMMIT":
			if session.active == nil {
				results = append(results, failedStatement(
					index, statement.Kind, source, result.CodeInvalidTransaction,
					"cannot COMMIT while transaction is idle",
				))
				continue
			}
			commitErr := session.active.Commit()
			session.active = nil
			if commitErr != nil {
				markRolledBack(results, transactionStart, "transaction commit failed")
				results = append(results, statementFailure(index, statement.Kind, source, commitErr))
			} else {
				results = append(results, result.NewStatement(index, statement.Kind, source))
			}
			transactionStart = -1
		case "ROLLBACK":
			if session.active == nil {
				results = append(results, failedStatement(
					index, statement.Kind, source, result.CodeInvalidTransaction,
					"cannot ROLLBACK while transaction is idle",
				))
				continue
			}
			rollbackErr := session.active.Rollback()
			session.active = nil
			markRolledBack(results, transactionStart, "transaction was explicitly rolled back")
			if rollbackErr != nil {
				results = append(results, statementFailure(index, statement.Kind, source, rollbackErr))
			} else {
				results = append(results, result.NewStatement(index, statement.Kind, source))
			}
			transactionStart = -1
		default:
			engine := session.autocommit
			if session.active != nil {
				engine = New(session.active, session.active)
			}
			executionContext := ctx
			if hasAuthorization(inputs[index].Authorization) {
				executionContext = security.WithAuthorization(ctx, inputs[index].Authorization)
			}
			output, executeErr := engine.Execute(
				executionContext, statement, inputs[index].Parameters, inputs[index].Mutation,
			)
			if executeErr == nil {
				results = append(results, successfulStatement(index, statement.Kind, source, output))
				continue
			}
			if session.active != nil && mutationStatement(statement) {
				rollbackErr := session.active.Rollback()
				session.active = nil
				markRolledBack(results, transactionStart, "transaction was rolled back after a write failure")
				if rollbackErr != nil {
					executeErr = rollbackErr
				}
				session.aborted = true
			}
			results = append(results, statementFailure(index, statement.Kind, source, executeErr))
		}
	}

	envelope := result.NewEnvelope(request.RequestID, results...)
	for _, statement := range results {
		if statement.Truncated {
			envelope.Truncated = true
		}
		if statement.NextCursor != "" {
			envelope.NextCursor = statement.NextCursor
		}
	}
	return envelope
}

func hasAuthorization(authorization security.Authorization) bool {
	return authorization.Version != "" || authorization.Actor != "" ||
		len(authorization.AuthorizedDatabases) > 0 || authorization.Approval != nil
}

func (session *BatchSession) Active() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.active != nil || session.aborted
}

func (session *BatchSession) Close() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil
	}
	session.closed = true
	var err error
	if session.active != nil {
		err = session.active.Rollback()
		session.active = nil
	}
	session.aborted = false
	session.cancel()
	return err
}

func statementInputs(supplied []StatementInput, count int) ([]StatementInput, bool) {
	if len(supplied) == 0 {
		return make([]StatementInput, count), true
	}
	if len(supplied) != count {
		return nil, false
	}
	return supplied, true
}

func successfulStatement(index int, kind, source string, output Output) result.StatementResult {
	statement := result.NewStatement(index, kind, source)
	statement.Columns = output.Columns
	statement.Rows = output.Rows
	statement.AffectedRows = output.AffectedRows
	statement.Revision = output.Revision
	statement.CommitSequence = output.CommitSequence
	statement.Truncated = output.Truncated
	statement.NextCursor = output.NextCursor
	statement.Page = output.Page
	statement.RowDetail = output.RowDetail
	statement.Discovery = output.Discovery
	return statement
}

func statementFailure(index int, kind, source string, err error) result.StatementResult {
	code, message := errorResult(err)
	return failedStatement(index, kind, source, code, message)
}

func failedStatement(index int, kind, source string, code result.Code, message string) result.StatementResult {
	return result.FailedStatement(index, kind, source, code, message, retryable(code))
}

func skippedStatement(index int, kind, source string) result.StatementResult {
	statement := failedStatement(
		index, kind, source, result.CodeTransactionAborted,
		"statement was skipped because its transaction was aborted",
	)
	statement.Status = result.StatusSkipped
	return statement
}

func markRolledBack(statements []result.StatementResult, start int, message string) {
	if start < 0 {
		start = 0
	}
	for index := start; index < len(statements); index++ {
		if statements[index].Status != result.StatusSucceeded {
			continue
		}
		failure := failedStatement(
			statements[index].Index,
			statements[index].Statement,
			statements[index].Source,
			result.CodeTransactionAborted,
			message,
		)
		statements[index].Status = result.StatusRolledBack
		statements[index].Error = failure.Error
	}
}

func errorResult(err error) (result.Code, string) {
	err = normalizeError(err)
	var executeErr *Error
	if errors.As(err, &executeErr) {
		return executeErr.Code, executeErr.Message
	}
	var stable interface{ StableCode() string }
	if errors.As(err, &stable) {
		code := result.Code(stable.StableCode())
		if result.IsRegisteredCode(code) {
			if code == result.CodeInternal {
				return code, "MSQL statement failed"
			}
			return code, err.Error()
		}
	}
	return result.CodeInternal, "MSQL statement failed"
}

func retryable(code result.Code) bool {
	return code == result.CodeWriteConflict || code == result.CodeCancelled || code == result.CodeDeadlineExceeded
}

func mutationStatement(statement ast.Statement) bool {
	return statement.Insert != nil || statement.Update != nil || statement.Delete != nil ||
		statement.Restore != nil || statement.Reshape != nil || statement.Relate != nil || statement.Unrelate != nil ||
		statement.CreateRoute != nil || statement.RenameRoute != nil || statement.UpdateRoute != nil || statement.DeleteRoute != nil ||
		statement.Configuration != nil ||
		(statement.Package != nil && statement.Package.Action == "INSTALL")
}

func spanSource(source string, span lexer.Span, fallback string) string {
	start, end := span.Start.Offset, span.End.Offset
	if start >= 0 && end >= start && end <= len(source) {
		if value := strings.TrimSpace(source[start:end]); value != "" {
			return value
		}
	}
	return fallback
}

func parseMessage(err error) string {
	var parseErr *parser.Error
	if errors.As(err, &parseErr) {
		return parseErr.Error()
	}
	return "MSQL request could not be parsed"
}

func parserResultCode(err *parser.Error) result.Code {
	if err.Code == parser.ErrorUnsupportedStatement {
		return result.CodeUnsupported
	}
	return result.CodeParseError
}

func mutationKind(kind string) bool {
	return kind == "INSERT" || kind == "UPDATE" || kind == "DELETE" || kind == "RESTORE" ||
		kind == "SPLIT" || kind == "MERGE" ||
		kind == "RELATE" || kind == "UNRELATE" || kind == "CREATE_ROUTE" ||
		kind == "RENAME_ROUTE" || kind == "UPDATE_ROUTE" || kind == "DELETE_ROUTE" ||
		kind == "ALTER_CONFIGURATION" || kind == "RESTORE_CONFIGURATION" || kind == "INSTALL_PACKAGE"
}
