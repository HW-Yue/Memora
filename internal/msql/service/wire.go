package service

import (
	"encoding/json"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func toBatchRequest(request protocolmsql.Request) executor.BatchRequest {
	statements := make([]executor.StatementInput, len(request.Statements))
	for index, statement := range request.Statements {
		statements[index] = executor.StatementInput{
			Parameters: executor.Parameters{
				Named:      cloneMap(statement.Parameters.Named),
				Positional: cloneSlice(statement.Parameters.Positional),
			},
			Mutation:      toMutationOptions(statement.Mutation),
			Authorization: toAuthorization(statement.Authorization),
		}
	}
	return executor.BatchRequest{Source: request.Source, Statements: statements}
}

func toMutationOptions(options protocolmsql.MutationOptions) executor.MutationOptions {
	routeUpdates := make([]row.RouteUpdate, len(options.RouteUpdates))
	for index, update := range options.RouteUpdates {
		routeUpdates[index] = row.RouteUpdate{
			RouteID: update.RouteID, ExpectedRevision: update.ExpectedRevision,
			Purpose: update.Purpose, Synopsis: cloneString(update.Synopsis),
		}
	}
	return executor.MutationOptions{
		ExpectedSchemaVersion:  options.ExpectedSchemaVersion,
		ExpectedRevision:       options.ExpectedRevision,
		SourceRevisions:        cloneMap(options.SourceRevisions),
		MaxAffectedRows:        options.MaxAffectedRows,
		Actor:                  options.Actor,
		Source:                 options.Source,
		Reason:                 options.Reason,
		SourceKind:             history.SourceKind(options.SourceKind),
		SourceReceiptID:        options.SourceReceiptID,
		SourceLocator:          options.SourceLocator,
		SourceContentHash:      options.SourceContentHash,
		RouteLeafIDs:           cloneSlice(options.RouteLeafIDs),
		TargetRouteLeafIDs:     cloneNestedStrings(options.TargetRouteLeafIDs),
		RelationTargetOrdinals: cloneMap(options.RelationTargetOrdinals),
		RouteUpdates:           routeUpdates,
	}
}

func toAuthorization(value protocolmsql.Authorization) security.Authorization {
	var approval *security.Approval
	if value.Approval != nil {
		approval = &security.Approval{
			Version: value.Approval.Version, Action: value.Approval.Action,
			SubjectSHA256: value.Approval.SubjectSHA256, Confirmed: value.Approval.Confirmed,
		}
	}
	levels := make(map[string]security.RiskLevel, len(value.DatabaseLevels))
	for database, level := range value.DatabaseLevels {
		levels[database] = security.RiskLevel(level)
	}
	if value.DatabaseLevels == nil {
		levels = nil
	}
	return security.Authorization{
		Version: value.Version, Actor: value.Actor,
		AuthorizedDatabases: cloneSlice(value.AuthorizedDatabases),
		DefaultLevel:        security.RiskLevel(value.DefaultLevel),
		DatabaseLevels:      levels,
		Approval:            approval,
	}
}

func toProtocolEnvelope(value result.Envelope) (protocolmsql.Envelope, error) {
	statements := make([]protocolmsql.StatementResult, len(value.Results))
	for index, statement := range value.Results {
		converted, err := toProtocolStatement(statement)
		if err != nil {
			return protocolmsql.Envelope{}, err
		}
		statements[index] = converted
	}
	envelope := protocolmsql.Envelope{
		Version: value.Version, RequestID: value.RequestID, OK: value.OK,
		Results: statements, Truncated: value.Truncated, NextCursor: value.NextCursor,
		Warnings: toProtocolNotices(value.Warnings), Error: toProtocolError(value.Error),
	}
	if err := envelope.Validate(); err != nil {
		return protocolmsql.Envelope{}, err
	}
	return envelope, nil
}

func toProtocolStatement(value result.StatementResult) (protocolmsql.StatementResult, error) {
	columns := make([]protocolmsql.Column, len(value.Columns))
	for index, column := range value.Columns {
		columns[index] = protocolmsql.Column{
			Name: column.Name, Type: column.Type, Nullable: column.Nullable,
			ColumnID: column.ColumnID, Purpose: column.Purpose, SemanticRole: column.SemanticRole,
		}
	}
	rows := make([]protocolmsql.Row, len(value.Rows))
	for index, source := range value.Rows {
		rows[index] = protocolmsql.Row(cloneMap(source))
	}
	var page *protocolmsql.ListPage
	if value.Page != nil {
		page = &protocolmsql.ListPage{
			Version: value.Page.Version, Limit: value.Page.Limit, Cursor: value.Page.Cursor,
			Snapshot: value.Page.Snapshot, Truncated: value.Page.Truncated,
			NextCursor: value.Page.NextCursor,
		}
	}
	rowDetail, err := marshalOpaque(value.RowDetail)
	if err != nil {
		return protocolmsql.StatementResult{}, err
	}
	discovery, err := marshalOpaque(value.Discovery)
	if err != nil {
		return protocolmsql.StatementResult{}, err
	}
	return protocolmsql.StatementResult{
		Index: value.Index, Statement: value.Statement, Source: value.Source,
		Status: protocolmsql.Status(value.Status), Columns: columns, Rows: rows,
		AffectedRows: value.AffectedRows, Revision: cloneUint64(value.Revision),
		CommitSequence: cloneUint64(value.CommitSequence), Truncated: value.Truncated,
		NextCursor: value.NextCursor, Page: page, RowDetail: rowDetail, Discovery: discovery,
		Warnings: toProtocolNotices(value.Warnings), Error: toProtocolError(value.Error),
	}, nil
}

func marshalOpaque(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

func toProtocolNotices(values []result.Notice) []protocolmsql.Notice {
	notices := make([]protocolmsql.Notice, len(values))
	for index, notice := range values {
		notices[index] = protocolmsql.Notice{
			Code: protocolmsql.Code(notice.Code), Message: notice.Message,
			Details: cloneMap(notice.Details),
		}
	}
	return notices
}

func toProtocolError(value *result.ResultError) *protocolmsql.ResultError {
	if value == nil {
		return nil
	}
	return &protocolmsql.ResultError{
		Code: protocolmsql.Code(value.Code), Message: value.Message, Retryable: value.Retryable,
		StatementIndex: cloneInt(value.StatementIndex), Details: cloneMap(value.Details),
	}
}

func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	if source == nil {
		return nil
	}
	value := make(map[K]V, len(source))
	for key, item := range source {
		value[key] = item
	}
	return value
}

func cloneSlice[T any](source []T) []T {
	if source == nil {
		return nil
	}
	return append([]T(nil), source...)
}

func cloneNestedStrings(source [][]string) [][]string {
	if source == nil {
		return nil
	}
	value := make([][]string, len(source))
	for index, item := range source {
		value[index] = cloneSlice(item)
	}
	return value
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneUint64(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
