package executor_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
)

func TestIndexedPointGetOwnsExactAutocommitSelectsWithoutLegacyFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	table := indexedPointTable()
	current := indexedPointRow(table, 3, 30, "current")
	historical := indexedPointRow(table, 1, 10, "historical")
	legacyCatalog := &poisonPointCatalog{}
	legacyRows := &poisonPointRows{}
	points := &stubPointReads{table: table, current: current, revision: historical, commit: historical}
	session := executor.NewBatchSessionWithPointReads(ctx, legacyCatalog, legacyRows, points)
	t.Cleanup(func() { _ = session.Close() })

	request := executor.BatchRequest{
		RequestID: "indexed-current",
		Source:    "SELECT title, revision FROM work.notes WHERE row_id = :row AND title = 'current' LIMIT 1",
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"row": current.ID}},
		}},
	}
	envelope := session.Execute(ctx, request)
	if !envelope.OK || len(envelope.Results) != 1 ||
		len(envelope.Results[0].Rows) != 1 ||
		envelope.Results[0].Rows[0]["title"] != "current" ||
		envelope.Results[0].Rows[0]["revision"] != uint64(3) {
		t.Fatalf("current envelope = %#v", envelope)
	}

	request.RequestID = "indexed-filter"
	request.Source = "SELECT title FROM work.notes WHERE row_id = :row AND title = 'other' LIMIT 1"
	filtered := session.Execute(ctx, request)
	if !filtered.OK || len(filtered.Results[0].Rows) != 0 {
		t.Fatalf("filtered envelope = %#v", filtered)
	}

	request.RequestID = "indexed-missing"
	request.Source = "SELECT title FROM work.notes WHERE row_id = 'row_missing' LIMIT 1"
	request.Statements = nil
	missing := session.Execute(ctx, request)
	if !missing.OK || len(missing.Results[0].Rows) != 0 {
		t.Fatalf("missing envelope = %#v", missing)
	}

	request.RequestID = "indexed-revision"
	request.Source = "SELECT title, revision FROM work.notes AS OF REVISION 1 WHERE row_id = :row LIMIT 1"
	request.Statements = []executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"row": current.ID}}}}
	atRevision := session.Execute(ctx, request)
	if !atRevision.OK || len(atRevision.Results[0].Rows) != 1 ||
		atRevision.Results[0].Rows[0]["title"] != "historical" {
		t.Fatalf("revision envelope = %#v", atRevision)
	}

	request.RequestID = "indexed-commit"
	request.Source = "SELECT title, commit_sequence FROM work.notes AS OF COMMIT_SEQUENCE 15 WHERE row_id = :row LIMIT 1"
	atCommit := session.Execute(ctx, request)
	if !atCommit.OK || len(atCommit.Results[0].Rows) != 1 ||
		atCommit.Results[0].Rows[0]["commit_sequence"] != uint64(10) {
		t.Fatalf("commit envelope = %#v", atCommit)
	}

	if legacyCatalog.calls != 0 || legacyRows.calls != 0 {
		t.Fatalf("legacy point path calls = catalog:%d rows:%d", legacyCatalog.calls, legacyRows.calls)
	}
	if points.tableCalls != 5 || points.getCalls != 3 || points.revisionCalls != 1 || points.commitCalls != 1 {
		t.Fatalf("indexed calls = %+v", points)
	}
}

func TestIndexedPointGetLeavesNonExactSelectOnLegacyScan(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	table := indexedPointTable()
	value := indexedPointRow(table, 1, 1, "current")
	dictionary := &legacyPointCatalog{table: table}
	rows := &legacyPointRows{values: []row.Row{value}}
	points := &stubPointReads{table: table, current: value}
	session := executor.NewBatchSessionWithPointReads(ctx, dictionary, rows, points)
	t.Cleanup(func() { _ = session.Close() })

	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "legacy-scan",
		Source:    "SELECT title FROM work.notes WHERE title = 'current' LIMIT 1",
	})
	if !envelope.OK || len(envelope.Results[0].Rows) != 1 ||
		envelope.Results[0].Rows[0]["title"] != "current" {
		t.Fatalf("non-exact envelope = %#v", envelope)
	}
	if dictionary.calls != 1 || rows.listCalls != 1 || points.tableCalls != 0 || points.getCalls != 0 {
		t.Fatalf("non-exact calls = catalog:%d list:%d points:%+v", dictionary.calls, rows.listCalls, points)
	}
}

type poisonPointCatalog struct {
	executor.Catalog
	calls int
}

func (dictionary *poisonPointCatalog) DescribeTable(context.Context, string, string) (catalog.Table, error) {
	dictionary.calls++
	return catalog.Table{}, fmt.Errorf("legacy DescribeTable must not be called")
}

type poisonPointRows struct {
	executor.Rows
	calls int
}

type legacyPointCatalog struct {
	executor.Catalog
	table catalog.Table
	calls int
}

func (dictionary *legacyPointCatalog) DescribeTable(context.Context, string, string) (catalog.Table, error) {
	dictionary.calls++
	return dictionary.table, nil
}

type legacyPointRows struct {
	executor.Rows
	values    []row.Row
	listCalls int
}

func (rows *legacyPointRows) ListPage(context.Context, string, string, int) ([]row.Row, bool, error) {
	rows.listCalls++
	return append([]row.Row(nil), rows.values...), false, nil
}

func (rows *poisonPointRows) Get(context.Context, string, string, string) (row.Row, error) {
	rows.calls++
	return row.Row{}, fmt.Errorf("legacy Get must not be called")
}

func (rows *poisonPointRows) AsOfRevision(context.Context, string, string, string, uint64) (row.Row, error) {
	rows.calls++
	return row.Row{}, fmt.Errorf("legacy AsOfRevision must not be called")
}

func (rows *poisonPointRows) AsOfCommit(context.Context, string, string, string, uint64) (row.Row, error) {
	rows.calls++
	return row.Row{}, fmt.Errorf("legacy AsOfCommit must not be called")
}

type stubPointReads struct {
	table                      catalog.Table
	current, revision, commit  row.Row
	tableCalls, getCalls       int
	revisionCalls, commitCalls int
}

func (reader *stubPointReads) DescribeTable(context.Context, string, string) (catalog.Table, error) {
	reader.tableCalls++
	return reader.table, nil
}

func (reader *stubPointReads) Get(_ context.Context, _ catalog.Table, rowID string) (row.Row, error) {
	reader.getCalls++
	if rowID == "row_missing" {
		return row.Row{}, pointTestError{code: result.CodeNotFound}
	}
	return reader.current, nil
}

func (reader *stubPointReads) AsOfRevision(context.Context, catalog.Table, string, uint64) (row.Row, error) {
	reader.revisionCalls++
	return reader.revision, nil
}

func (reader *stubPointReads) AsOfCommit(context.Context, catalog.Table, string, uint64) (row.Row, error) {
	reader.commitCalls++
	return reader.commit, nil
}

type pointTestError struct{ code result.Code }

func (err pointTestError) Error() string      { return string(err.code) }
func (err pointTestError) StableCode() string { return string(err.code) }

func indexedPointTable() catalog.Table {
	return catalog.Table{
		ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Aliases: []string{},
		Purpose: "Notes", RowSemantics: "One note", SchemaVersion: 1,
		Columns: []catalog.Column{{
			ID: "col_title", Name: "title", Aliases: []string{}, Type: "TEXT",
			MaxCharacters: 100, Purpose: "Title", SchemaVersion: 1,
		}},
	}
}

func indexedPointRow(table catalog.Table, revision, sequence uint64, title string) row.Row {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	return row.Row{
		ID: "row_one", DatabaseID: table.DatabaseID, TableID: table.ID,
		SchemaVersion: table.SchemaVersion, Revision: revision, CommitSequence: sequence,
		State: row.StateLive, Values: map[string]any{"title": title},
		CreatedAt: now, UpdatedAt: now,
	}
}
