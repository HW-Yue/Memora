package executor_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routetrace"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestShowRouteTracesPinsSnapshotAndKeepsStepsOutOfTimeline(t *testing.T) {
	ctx := context.Background()
	dictionary, baseRows, closeStore := changeDependencies(t, ctx)
	defer closeStore()
	rows, closeTraces := traceReadFixture(t, baseRows)
	defer closeTraces()
	for number := 1; number <= 3; number++ {
		if _, err := rows.traces.Record(ctx, traceDraft(number)); err != nil {
			t.Fatal(err)
		}
	}
	subject := executor.New(dictionary, rows)
	first, err := execute(ctx, subject, "SHOW ROUTE TRACES IN DATABASE work LIMIT 1", executor.Parameters{}, executor.MutationOptions{})
	if err != nil || len(first.Rows) != 1 || first.Page == nil || !first.Page.Truncated {
		t.Fatalf("first trace Page = %#v, %v", first, err)
	}
	if _, leaked := first.Rows[0]["steps"]; leaked {
		t.Fatalf("trace timeline leaked steps: %#v", first.Rows[0])
	}
	_, err = execute(ctx, subject, "SHOW ROUTE TRACES CURSOR :cursor LIMIT 1", executor.Parameters{
		Named: map[string]any{"cursor": first.Page.NextCursor},
	}, executor.MutationOptions{})
	assertExecutorCode(t, err, result.CodeValidation)
	if _, err := rows.traces.Record(ctx, traceDraft(4)); err != nil {
		t.Fatal(err)
	}
	tail, err := execute(ctx, subject,
		"SHOW ROUTE TRACES IN DATABASE work CURSOR :cursor LIMIT 10",
		executor.Parameters{Named: map[string]any{"cursor": first.Page.NextCursor}}, executor.MutationOptions{},
	)
	if err != nil || len(tail.Rows) != 2 || tail.Page == nil || tail.Page.Truncated || tail.Rows[1]["trace_sequence"] != uint64(3) {
		t.Fatalf("trace tail = %#v, %v", tail, err)
	}
	fresh, err := execute(ctx, subject, "SHOW ROUTE TRACES IN DATABASE work LIMIT 10", executor.Parameters{}, executor.MutationOptions{})
	if err != nil || len(fresh.Rows) != 4 {
		t.Fatalf("fresh trace Page = %#v, %v", fresh, err)
	}
}

func TestShowRouteTracePaginatesStepsAndRejectsCursorTamper(t *testing.T) {
	ctx := context.Background()
	dictionary, baseRows, closeStore := changeDependencies(t, ctx)
	defer closeStore()
	rows, closeTraces := traceReadFixture(t, baseRows)
	defer closeTraces()
	created, err := rows.traces.Record(ctx, traceDraft(1))
	if err != nil {
		t.Fatal(err)
	}
	subject := executor.New(dictionary, rows)
	first, err := execute(ctx, subject,
		"SHOW ROUTE TRACE :trace IN DATABASE work LIMIT 1",
		executor.Parameters{Named: map[string]any{"trace": created.TraceID}}, executor.MutationOptions{},
	)
	if err != nil || len(first.Rows) != 1 || first.Page == nil || !first.Page.Truncated {
		t.Fatalf("first trace steps = %#v, %v", first, err)
	}
	for _, forbidden := range []string{"prompt", "question", "values", "purpose", "description", "reasoning"} {
		if _, leaked := first.Rows[0][forbidden]; leaked {
			t.Fatalf("trace step leaked %q: %#v", forbidden, first.Rows[0])
		}
	}
	tail, err := execute(ctx, subject,
		"SHOW ROUTE TRACE :trace IN DATABASE work CURSOR :cursor LIMIT 1",
		executor.Parameters{Named: map[string]any{
			"trace": created.TraceID, "cursor": first.Page.NextCursor,
		}}, executor.MutationOptions{},
	)
	if err != nil || len(tail.Rows) != 1 || tail.Page == nil || tail.Page.Truncated {
		t.Fatalf("trace step tail = %#v, %v", tail, err)
	}
	tampered := first.Page.NextCursor[:len(first.Page.NextCursor)-1] + "A"
	_, err = execute(ctx, subject,
		"SHOW ROUTE TRACE :trace IN DATABASE work CURSOR :cursor LIMIT 1",
		executor.Parameters{Named: map[string]any{"trace": created.TraceID, "cursor": tampered}}, executor.MutationOptions{},
	)
	assertExecutorCode(t, err, result.CodeValidation)
	other, err := rows.traces.Record(ctx, traceDraft(2))
	if err != nil {
		t.Fatal(err)
	}
	_, err = execute(ctx, subject,
		"SHOW ROUTE TRACE :trace IN DATABASE work CURSOR :cursor LIMIT 1",
		executor.Parameters{Named: map[string]any{
			"trace": other.TraceID, "cursor": first.Page.NextCursor,
		}}, executor.MutationOptions{},
	)
	assertExecutorCode(t, err, result.CodeValidation)
}

func TestShowRouteTracesRejectsInvalidBoundsAndMixedCursor(t *testing.T) {
	ctx := context.Background()
	dictionary, baseRows, closeStore := changeDependencies(t, ctx)
	defer closeStore()
	rows, closeTraces := traceReadFixture(t, baseRows)
	defer closeTraces()
	for number := 1; number <= 2; number++ {
		if _, err := rows.traces.Record(ctx, traceDraft(number)); err != nil {
			t.Fatal(err)
		}
	}
	subject := executor.New(dictionary, rows)
	first, err := execute(ctx, subject, "SHOW ROUTE TRACES IN DATABASE work LIMIT 1", executor.Parameters{}, executor.MutationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		source     string
		parameters executor.Parameters
	}{
		{"SHOW ROUTE TRACES IN DATABASE work AFTER TRACE_SEQUENCE -1 LIMIT 1", executor.Parameters{}},
		{"SHOW ROUTE TRACES IN DATABASE work LIMIT 257", executor.Parameters{}},
		{
			"SHOW ROUTE TRACES IN DATABASE work AFTER TRACE_SEQUENCE 1 CURSOR :cursor LIMIT 1",
			executor.Parameters{Named: map[string]any{"cursor": first.Page.NextCursor}},
		},
	} {
		_, err := execute(ctx, subject, test.source, test.parameters, executor.MutationOptions{})
		assertExecutorCode(t, err, result.CodeValidation)
	}
}

func TestRouteTracePruneInvalidatesTimelineCursor(t *testing.T) {
	ctx := context.Background()
	dictionary, baseRows, closeStore := changeDependencies(t, ctx)
	defer closeStore()
	rows, closeTraces := traceReadFixture(t, baseRows)
	defer closeTraces()
	for number := 1; number <= 2; number++ {
		if _, err := rows.traces.Record(ctx, traceDraft(number)); err != nil {
			t.Fatal(err)
		}
	}
	subject := executor.New(dictionary, rows)
	first, err := execute(ctx, subject, "SHOW ROUTE TRACES IN DATABASE work LIMIT 1", executor.Parameters{}, executor.MutationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rows.traces.PruneExpired(ctx, time.Unix(10_000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	_, err = execute(ctx, subject,
		"SHOW ROUTE TRACES IN DATABASE work CURSOR :cursor LIMIT 1",
		executor.Parameters{Named: map[string]any{"cursor": first.Page.NextCursor}}, executor.MutationOptions{},
	)
	assertExecutorCode(t, err, result.CodeRevisionConflict)
}

func TestScopedAuthorizationRequiresExplicitRouteTraceDatabase(t *testing.T) {
	ctx := context.Background()
	dictionary, baseRows, closeStore := changeDependencies(t, ctx)
	defer closeStore()
	rows, closeTraces := traceReadFixture(t, baseRows)
	defer closeTraces()
	if _, err := rows.traces.Record(ctx, traceDraft(1)); err != nil {
		t.Fatal(err)
	}
	subject := executor.New(dictionary, rows)
	scoped := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:test",
		AuthorizedDatabases: []string{"db_database"},
	})
	_, err := execute(scoped, subject, "SHOW ROUTE TRACES LIMIT 10", executor.Parameters{}, executor.MutationOptions{})
	assertExecutorCode(t, err, result.CodePermissionDenied)
	if _, err := execute(scoped, subject, "SHOW ROUTE TRACES IN DATABASE work LIMIT 10", executor.Parameters{}, executor.MutationOptions{}); err != nil {
		t.Fatalf("scoped SHOW ROUTE TRACES error = %v", err)
	}
}

type traceReadRows struct {
	executor.Rows
	traces *routetrace.Service
}

func (rows *traceReadRows) ListRouteTraces(
	ctx context.Context, databaseID string, after uint64, snapshot routetrace.Snapshot, limit int,
) ([]routetrace.Trace, routetrace.Snapshot, bool, error) {
	return rows.traces.List(ctx, databaseID, after, snapshot, limit)
}

func (rows *traceReadRows) GetRouteTrace(ctx context.Context, traceID, databaseID string) (routetrace.Trace, error) {
	return rows.traces.Get(ctx, traceID, databaseID)
}

func traceReadFixture(t *testing.T, rows executor.Rows) (*traceReadRows, func()) {
	t.Helper()
	database, err := nativekv.Open(filepath.Join(t.TempDir(), "traces.memora"))
	if err != nil {
		t.Fatal(err)
	}
	return &traceReadRows{Rows: rows, traces: routetrace.New(database)}, func() {
		if err := database.Close(); err != nil {
			t.Errorf("Close traces: %v", err)
		}
	}
}

func traceDraft(number int) routetrace.Draft {
	recorded := time.Unix(int64(number), 0).UTC()
	return routetrace.Draft{
		TraceID: fmt.Sprintf("trace_%032x", number), RecordedAt: recorded,
		ExpiresAt: recorded.Add(time.Hour), Actor: "agent:test",
		DatabaseID: "db_database", TableID: "tbl_table", Status: routetrace.StatusComplete,
		Steps: []routetrace.Step{
			{Ordinal: 1, Operation: routetrace.OperationFrame, ParentRouteID: "route_root",
				CandidateRouteIDs: []string{"route_arch", "route_other"}, SelectedRouteID: "route_arch",
				Outcome: routetrace.OutcomeSucceeded, ElapsedMillis: 3, RemainingBudget: 11},
			{Ordinal: 2, Operation: routetrace.OperationOpen, SelectedRouteID: "route_arch",
				Locators: []routetrace.Locator{{RowID: fmt.Sprintf("row_%d", number), Revision: 2}},
				Outcome:  routetrace.OutcomeSucceeded, ElapsedMillis: 2, RemainingBudget: 9},
		},
	}
}
