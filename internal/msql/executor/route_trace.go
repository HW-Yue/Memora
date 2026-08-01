package executor

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routetrace"
	"github.com/HW-Yue/Memora/internal/security"
)

type routeTraceReads interface {
	ListRouteTraces(context.Context, string, uint64, routetrace.Snapshot, int) ([]routetrace.Trace, routetrace.Snapshot, bool, error)
	GetRouteTrace(context.Context, string, string) (routetrace.Trace, error)
}

func (engine *Engine) showRouteTraces(
	ctx context.Context, show *ast.ShowStatement, bound bindings,
) (Output, error) {
	reads, ok := engine.rows.(routeTraceReads)
	if !ok {
		return Output{}, executeError(result.CodeUnsupported, "Route Trace reads require an observation store")
	}
	databaseID, scope, err := engine.routeTraceReadScope(ctx, show, routeTraceTimelineKind)
	if err != nil {
		return Output{}, err
	}
	limit, err := routeTraceLimit(show.Limit, bound)
	if err != nil {
		return Output{}, err
	}
	if show.After != nil && show.Cursor != nil {
		return Output{}, executeError(result.CodeValidation, "SHOW ROUTE TRACES AFTER and CURSOR are mutually exclusive")
	}
	after, snapshot, cursor := uint64(0), routetrace.Snapshot{}, ""
	if show.After != nil {
		after, err = changeNonnegativeInteger(show.After, bound, "SHOW ROUTE TRACES AFTER TRACE_SEQUENCE")
		if err != nil {
			return Output{}, err
		}
	}
	if show.Cursor != nil {
		cursor, err = relationshipString(show.Cursor, catalog.Table{}, bound, "Route Trace cursor")
		if err != nil || cursor == "" || len(cursor) > routeTraceCursorMaximum {
			return Output{}, executeError(result.CodeValidation, "Route Trace cursor is invalid")
		}
		decoded, decodeErr := decodeRouteTraceCursor(cursor)
		if decodeErr != nil || decoded.Kind != routeTraceTimelineKind || decoded.Scope != scope {
			return Output{}, executeError(result.CodeValidation, "Route Trace cursor is invalid for this timeline")
		}
		after = decoded.Position
		snapshot = routetrace.Snapshot{
			HighWater: decoded.HighWater, RetentionEpoch: decoded.RetentionEpoch,
		}
	}
	values, snapshot, more, err := reads.ListRouteTraces(ctx, databaseID, after, snapshot, int(limit))
	if err != nil {
		return Output{}, normalizeRouteTraceError(err)
	}
	next := ""
	if more {
		if len(values) == 0 {
			return Output{}, executeError(result.CodeInternal, "Route Trace timeline returned an empty continuation Page")
		}
		next, err = encodeRouteTraceCursor(routeTraceCursorCore{
			Version: routeTraceCursorVersion, Kind: routeTraceTimelineKind, Scope: scope,
			HighWater: snapshot.HighWater, RetentionEpoch: snapshot.RetentionEpoch,
			Position: values[len(values)-1].Sequence,
		})
		if err != nil {
			return Output{}, executeError(result.CodeInternal, "Route Trace cursor could not be encoded")
		}
	}
	output := Output{
		Columns: routeTraceTimelineColumns(), Rows: make([]result.Row, 0, len(values)),
		Truncated: more, NextCursor: next,
		Page: &result.ListPage{
			Version: result.ListPageVersion, Limit: limit, Cursor: cursor,
			Snapshot:  routeTraceSnapshot(scope, snapshot.HighWater, snapshot.RetentionEpoch, ""),
			Truncated: more, NextCursor: next,
		},
	}
	for _, value := range values {
		output.Rows = append(output.Rows, result.Row{
			"version": value.Version, "trace_id": value.TraceID, "trace_sequence": value.Sequence,
			"recorded_at": value.RecordedAt, "expires_at": value.ExpiresAt,
			"actor": value.Actor, "database_id": value.DatabaseID, "table_id": value.TableID,
			"status": string(value.Status), "step_count": uint64(len(value.Steps)), "checksum": value.Checksum,
		})
	}
	return output, nil
}

func (engine *Engine) showRouteTrace(
	ctx context.Context, show *ast.ShowStatement, bound bindings,
) (Output, error) {
	reads, ok := engine.rows.(routeTraceReads)
	if !ok {
		return Output{}, executeError(result.CodeUnsupported, "Route Trace reads require an observation store")
	}
	databaseID, scope, err := engine.routeTraceReadScope(ctx, show, routeTraceStepsKind)
	if err != nil {
		return Output{}, err
	}
	limit, err := routeTraceLimit(show.Limit, bound)
	if err != nil {
		return Output{}, err
	}
	traceID, err := relationshipString(show.Trace, catalog.Table{}, bound, "Route Trace ID")
	if err != nil || !validRouteTraceID(traceID) {
		return Output{}, executeError(result.CodeValidation, "Route Trace ID is invalid")
	}
	value, err := reads.GetRouteTrace(ctx, traceID, databaseID)
	if err != nil {
		return Output{}, normalizeRouteTraceError(err)
	}
	offset, cursor := uint64(0), ""
	if show.Cursor != nil {
		cursor, err = relationshipString(show.Cursor, catalog.Table{}, bound, "Route Trace cursor")
		if err != nil || cursor == "" || len(cursor) > routeTraceCursorMaximum {
			return Output{}, executeError(result.CodeValidation, "Route Trace cursor is invalid")
		}
		decoded, decodeErr := decodeRouteTraceCursor(cursor)
		if decodeErr != nil || decoded.Kind != routeTraceStepsKind || decoded.Scope != scope ||
			decoded.TraceID != value.TraceID || decoded.TraceChecksum != value.Checksum ||
			decoded.HighWater != value.Sequence {
			return Output{}, executeError(result.CodeValidation, "Route Trace cursor is invalid for this trace")
		}
		offset = decoded.Offset
		if offset >= uint64(len(value.Steps)) {
			return Output{}, executeError(result.CodeValidation, "Route Trace cursor offset is invalid")
		}
	}
	end := min(uint64(len(value.Steps)), offset+limit)
	more := end < uint64(len(value.Steps))
	next := ""
	if more {
		next, err = encodeRouteTraceCursor(routeTraceCursorCore{
			Version: routeTraceCursorVersion, Kind: routeTraceStepsKind, Scope: scope,
			HighWater: value.Sequence, TraceID: value.TraceID, TraceChecksum: value.Checksum, Offset: end,
		})
		if err != nil {
			return Output{}, executeError(result.CodeInternal, "Route Trace cursor could not be encoded")
		}
	}
	output := Output{
		Columns: routeTraceStepColumns(), Rows: make([]result.Row, 0, end-offset),
		Truncated: more, NextCursor: next,
		Page: &result.ListPage{
			Version: result.ListPageVersion, Limit: limit, Cursor: cursor,
			Snapshot:  routeTraceSnapshot(scope, value.Sequence, 0, value.Checksum),
			Truncated: more, NextCursor: next,
		},
	}
	for _, step := range value.Steps[offset:end] {
		output.Rows = append(output.Rows, result.Row{
			"trace_id": value.TraceID, "trace_sequence": value.Sequence, "ordinal": step.Ordinal,
			"operation": string(step.Operation), "parent_route_id": step.ParentRouteID,
			"candidate_route_ids": append([]string(nil), step.CandidateRouteIDs...),
			"selected_route_id":   step.SelectedRouteID,
			"locators":            append([]routetrace.Locator(nil), step.Locators...),
			"outcome":             string(step.Outcome), "elapsed_ms": step.ElapsedMillis,
			"remaining_budget": step.RemainingBudget,
		})
	}
	return output, nil
}

func (engine *Engine) routeTraceReadScope(
	ctx context.Context, show *ast.ShowStatement, kind string,
) (string, string, error) {
	if show.Database == nil {
		if _, present := security.AuthorizationFrom(ctx); present {
			return "", "", executeError(result.CodePermissionDenied, "scoped Authorization requires IN DATABASE for Route Traces")
		}
		return "", routeTraceScope(kind, ""), nil
	}
	if len(show.Database.Parts) != 1 || engine.catalog == nil {
		return "", "", executeError(result.CodeValidation, "Route Trace Database must be one Database name")
	}
	database, err := engine.catalog.DescribeDatabase(ctx, show.Database.Parts[0].Value)
	if err != nil {
		return "", "", normalizeError(err)
	}
	return database.ID, routeTraceScope(kind, database.ID), nil
}

func routeTraceLimit(expression *ast.Expression, bound bindings) (uint64, error) {
	limit, err := historyPositiveInteger(expression, catalog.Table{}, bound, "Route Trace LIMIT")
	if err != nil {
		return 0, err
	}
	if limit > 256 {
		return 0, executeError(result.CodeValidation, "Route Trace LIMIT must be between 1 and 256")
	}
	return limit, nil
}

func validRouteTraceID(value string) bool {
	if len(value) != 38 || !strings.HasPrefix(value, "trace_") || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value[6:])
	return err == nil && len(decoded) == 16
}

func normalizeRouteTraceError(err error) error {
	switch {
	case errors.Is(err, routetrace.ErrNotFound):
		return executeError(result.CodeNotFound, "Route Trace was not found")
	case errors.Is(err, routetrace.ErrInvalid):
		return executeError(result.CodeValidation, "Route Trace request is invalid")
	case errors.Is(err, routetrace.ErrSnapshotExpired):
		return executeError(result.CodeRevisionConflict, "Route Trace snapshot expired after retention cleanup")
	default:
		return normalizeError(err)
	}
}

func routeTraceTimelineColumns() []result.Column {
	return []result.Column{
		{Name: "version", Type: "TEXT"}, {Name: "trace_id", Type: "ID"},
		{Name: "trace_sequence", Type: "INTEGER"}, {Name: "recorded_at", Type: "TIMESTAMP"},
		{Name: "expires_at", Type: "TIMESTAMP"}, {Name: "actor", Type: "TEXT"},
		{Name: "database_id", Type: "ID"}, {Name: "table_id", Type: "ID"},
		{Name: "status", Type: "TEXT"}, {Name: "step_count", Type: "INTEGER"},
		{Name: "checksum", Type: "TEXT"},
	}
}

func routeTraceStepColumns() []result.Column {
	return []result.Column{
		{Name: "trace_id", Type: "ID"}, {Name: "trace_sequence", Type: "INTEGER"},
		{Name: "ordinal", Type: "INTEGER"}, {Name: "operation", Type: "TEXT"},
		{Name: "parent_route_id", Type: "ID", Nullable: true},
		{Name: "candidate_route_ids", Type: "ID_LIST"},
		{Name: "selected_route_id", Type: "ID", Nullable: true},
		{Name: "locators", Type: "ROW_LOCATOR_LIST"}, {Name: "outcome", Type: "TEXT"},
		{Name: "elapsed_ms", Type: "INTEGER"}, {Name: "remaining_budget", Type: "INTEGER"},
	}
}
