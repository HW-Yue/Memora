package nativerow

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestNativeInsertAndExactSelectMSQLSurviveReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 14, 0, 0, 123456789, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs:   &testIDs{values: []string{"database", "table", "title", "optional"}},
		Clock: testClock{value: now},
	})
	rows := NewService(New(file), dictionary, ServiceOptions{IDs: &testIDs{values: []string{"first"}}, Clock: testClock{value: now}})
	engine := executor.New(dictionary, rows)
	executeMSQL(t, ctx, engine, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(20) NOT NULL PURPOSE 'Title', optional TEXT(20) NULL PURPOSE 'Optional')", executor.Parameters{}, executor.MutationOptions{})
	inserted := executeMSQL(t, ctx, engine,
		"INSERT INTO work.notes (title, optional) VALUES (:title, :optional)",
		executor.Parameters{Named: map[string]any{"title": "原生知识", "optional": nil}},
		executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1},
	)
	rowID, ok := inserted.Rows[0]["row_id"].(string)
	if !ok || rowID != "row_first" {
		t.Fatalf("INSERT RowID = %#v", inserted.Rows)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	dictionary = nativecatalog.NewService(nativecatalog.New(reopened), nativecatalog.ServiceOptions{})
	rows = NewService(New(reopened), dictionary, ServiceOptions{})
	engine = executor.New(dictionary, rows)
	selected := executeMSQL(t, ctx, engine,
		"SELECT title, optional FROM work.notes WHERE row_id = :row_id LIMIT 1",
		executor.Parameters{Named: map[string]any{"row_id": rowID}}, executor.MutationOptions{},
	)
	if len(selected.Rows) != 1 || selected.Rows[0]["title"] != "原生知识" || selected.Rows[0]["optional"] != nil {
		t.Fatalf("SELECT rows = %#v", selected.Rows)
	}
	missing := executeMSQL(t, ctx, engine,
		"SELECT title FROM work.notes WHERE row_id = :row_id LIMIT 1",
		executor.Parameters{Named: map[string]any{"row_id": "row_missing"}}, executor.MutationOptions{},
	)
	if len(missing.Rows) != 0 {
		t.Fatalf("missing SELECT rows = %#v", missing.Rows)
	}
	defaultHistory := executeMSQL(t, ctx, engine,
		"SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 10",
		executor.Parameters{Named: map[string]any{"row": rowID}}, executor.MutationOptions{},
	)
	if defaultHistory.Rows[0]["source_kind"] != "conversation_assertion" ||
		defaultHistory.Rows[0]["source_receipt_id"] != "" {
		t.Fatalf("ordinary INSERT provenance = %#v", defaultHistory.Rows)
	}
	reviewed := executeMSQL(t, ctx, engine,
		"INSERT INTO work.notes (title, optional) VALUES (:title, :optional)",
		executor.Parameters{Named: map[string]any{"title": "reviewed", "optional": nil}},
		executor.MutationOptions{
			ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
			Actor: "agent:review", Source: "submission-1", Reason: "reviewed import",
			SourceKind: "reviewed_source", SourceReceiptID: "submission-1",
			SourceLocator:     "fixture/book.md",
			SourceContentHash: "sha256:" + strings.Repeat("a", 64),
		},
	)
	reviewedID, _ := reviewed.Rows[0]["row_id"].(string)
	reviewedHistory := executeMSQL(t, ctx, engine,
		"SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 10",
		executor.Parameters{Named: map[string]any{"row": reviewedID}}, executor.MutationOptions{},
	)
	if reviewedHistory.Rows[0]["source_kind"] != "reviewed_source" ||
		reviewedHistory.Rows[0]["source_receipt_id"] != "submission-1" {
		t.Fatalf("reviewed INSERT provenance = %#v", reviewedHistory.Rows)
	}
}

func TestNativeUpdateDeleteMSQLUseExpectedRevision(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 15, 0, 0, 0, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs: &testIDs{values: []string{"database", "table", "title"}}, Clock: testClock{value: now},
	})
	rows := NewService(New(file), dictionary, ServiceOptions{IDs: &testIDs{values: []string{"first"}}, Clock: testClock{value: now.Add(time.Minute)}})
	engine := executor.New(dictionary, rows)
	executeMSQL(t, ctx, engine, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(20) NOT NULL PURPOSE 'Title')", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "INSERT INTO work.notes (title) VALUES ('first')", executor.Parameters{}, executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1})

	updated := executeMSQL(t, ctx, engine,
		"UPDATE work.notes SET title = :title WHERE row_id = :row_id",
		executor.Parameters{Named: map[string]any{"title": "second", "row_id": "row_first"}},
		executor.MutationOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1},
	)
	if updated.Revision == nil || *updated.Revision != 2 {
		t.Fatalf("UPDATE revision = %#v", updated.Revision)
	}
	_, err = runMSQL(ctx, engine,
		"UPDATE work.notes SET title = 'stale' WHERE row_id = 'row_first'", executor.Parameters{},
		executor.MutationOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1},
	)
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) || stable.StableCode() != string(result.CodeRevisionConflict) {
		t.Fatalf("stale UPDATE error = %v", err)
	}
	deleted := executeMSQL(t, ctx, engine,
		"DELETE FROM work.notes WHERE row_id = 'row_first'", executor.Parameters{},
		executor.MutationOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 2, MaxAffectedRows: 1},
	)
	if deleted.Revision == nil || *deleted.Revision != 3 {
		t.Fatalf("DELETE revision = %#v", deleted.Revision)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	dictionary = nativecatalog.NewService(nativecatalog.New(reopened), nativecatalog.ServiceOptions{})
	rows = NewService(New(reopened), dictionary, ServiceOptions{})
	selected := executeMSQL(t, ctx, executor.New(dictionary, rows),
		"SELECT title FROM work.notes WHERE row_id = 'row_first' LIMIT 1",
		executor.Parameters{}, executor.MutationOptions{},
	)
	if len(selected.Rows) != 0 {
		t.Fatalf("SELECT after DELETE = %#v", selected.Rows)
	}
	tombstone, err := New(reopened).ReadIncludingDeleted("row_first")
	if err != nil || tombstone.Revision != 3 || tombstone.State != "deleted" {
		t.Fatalf("tombstone = %#v, %v", tombstone, err)
	}
}

func TestNativeHistoryMSQLSurvivesReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 16, 0, 0, 0, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs: &testIDs{values: []string{"database", "table", "title"}}, Clock: testClock{value: now},
	})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Projects"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{Name: "notes", Purpose: "Notes", RowSemantics: "One note", Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(20)", Purpose: "Title"}}}); err != nil {
		t.Fatal(err)
	}
	rows := NewService(New(file), dictionary, ServiceOptions{IDs: &testIDs{values: []string{"first"}}, Clock: testClock{value: now.Add(time.Minute)}})
	inserted, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "initial"}, row.WriteOptions{ExpectedSchemaVersion: 1, Metadata: row.WriteMetadata{Actor: "agent:writer", Source: "conversation", Reason: "remember"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Update(ctx, "work", "notes", inserted.ID, map[string]any{"title": "revised"}, row.WriteOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1, Metadata: row.WriteMetadata{Actor: "agent:editor", Source: "feedback", Reason: "correct"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Delete(ctx, "work", "notes", inserted.ID, row.WriteOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 2, Metadata: row.WriteMetadata{Actor: "agent:editor", Source: "feedback", Reason: "remove"}}); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	dictionary = nativecatalog.NewService(nativecatalog.New(reopened), nativecatalog.ServiceOptions{})
	rows = NewService(New(reopened), dictionary, ServiceOptions{})
	engine := executor.New(dictionary, rows)
	shown := executeMSQL(t, ctx, engine,
		"SHOW HISTORY FROM work.notes FOR ROW :row_id LIMIT 10",
		executor.Parameters{Named: map[string]any{"row_id": inserted.ID}}, executor.MutationOptions{},
	)
	if len(shown.Rows) != 3 || shown.Rows[0]["operation"] != "DELETE" || shown.Rows[1]["operation"] != "UPDATE" || shown.Rows[2]["actor"] != "agent:writer" {
		t.Fatalf("SHOW HISTORY rows = %#v", shown.Rows)
	}
	atFirst := executeMSQL(t, ctx, engine,
		"SELECT title, revision FROM work.notes AS OF REVISION 1 WHERE row_id = :row_id LIMIT 1",
		executor.Parameters{Named: map[string]any{"row_id": inserted.ID}}, executor.MutationOptions{},
	)
	if len(atFirst.Rows) != 1 || atFirst.Rows[0]["title"] != "initial" || atFirst.Rows[0]["revision"] != uint64(1) {
		t.Fatalf("AS OF REVISION rows = %#v", atFirst.Rows)
	}
}

func TestNativeRelationsMSQLTraverseBothDirectionsAfterReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 17, 0, 0, 0, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs: &testIDs{values: []string{"database", "table", "title", "private", "private_table", "private_title"}}, Clock: testClock{value: now},
	})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Projects"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{Name: "notes", Purpose: "Notes", RowSemantics: "One note", Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(20)", Purpose: "Title"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "private", Purpose: "Private", Scope: "Personal"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "private", catalog.TableDefinition{Name: "notes", Purpose: "Notes", RowSemantics: "One note", Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(20)", Purpose: "Title"}}}); err != nil {
		t.Fatal(err)
	}
	rows := NewService(New(file), dictionary, ServiceOptions{IDs: &testIDs{values: []string{"source", "target", "foreign", "edge"}}, Clock: testClock{value: now}})
	for _, title := range []string{"source", "target"} {
		if _, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": title}, row.WriteOptions{ExpectedSchemaVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := rows.Insert(ctx, "private", "notes", map[string]any{"title": "foreign"}, row.WriteOptions{ExpectedSchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Relate(ctx, row.RelationDefinition{
		Source: row.RelationEndpoint{Database: "work", Table: "notes", RowID: "row_source"},
		Type:   "crosses", Target: row.RelationEndpoint{Database: "private", Table: "notes", RowID: "row_foreign"},
	}); err == nil {
		t.Fatal("cross-database relation unexpectedly succeeded")
	}
	engine := executor.New(dictionary, rows)
	created := executeMSQL(t, ctx, engine,
		"RELATE work.notes ROW :source TO work.notes ROW :target TYPE :type DESCRIPTION :description",
		executor.Parameters{Named: map[string]any{"source": "row_source", "target": "row_target", "type": "depends_on", "description": "semantic edge"}},
		executor.MutationOptions{MaxAffectedRows: 1},
	)
	if created.Revision == nil || *created.Revision != 1 {
		t.Fatalf("RELATE output = %#v", created)
	}
	if _, err := runMSQL(ctx, engine,
		"RELATE work.notes ROW 'row_source' TO work.notes ROW 'row_target' TYPE 'depends_on'",
		executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1}); err == nil {
		t.Fatal("duplicate RELATE unexpectedly succeeded")
	}
	if _, err := runMSQL(ctx, engine,
		"RELATE work.notes ROW 'row_missing' TO work.notes ROW 'row_target' TYPE 'depends_on'",
		executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1}); err == nil {
		t.Fatal("dangling RELATE unexpectedly succeeded")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	dictionary = nativecatalog.NewService(nativecatalog.New(reopened), nativecatalog.ServiceOptions{})
	rows = NewService(New(reopened), dictionary, ServiceOptions{})
	engine = executor.New(dictionary, rows)
	outgoing := executeMSQL(t, ctx, engine,
		"SHOW RELATIONS FROM work.notes FOR ROW 'row_source' DIRECTION OUTGOING LIMIT 10",
		executor.Parameters{}, executor.MutationOptions{},
	)
	incoming := executeMSQL(t, ctx, engine,
		"SHOW RELATIONS FROM work.notes FOR ROW 'row_target' DIRECTION INCOMING LIMIT 10",
		executor.Parameters{}, executor.MutationOptions{},
	)
	if len(outgoing.Rows) != 1 || len(incoming.Rows) != 1 || outgoing.Rows[0]["relation_id"] != incoming.Rows[0]["relation_id"] {
		t.Fatalf("relation directions = outgoing %#v, incoming %#v", outgoing.Rows, incoming.Rows)
	}
	relationID := outgoing.Rows[0]["relation_id"].(string)
	deleted := executeMSQL(t, ctx, engine, "UNRELATE :relation_id",
		executor.Parameters{Named: map[string]any{"relation_id": relationID}},
		executor.MutationOptions{ExpectedRevision: 1, MaxAffectedRows: 1},
	)
	if deleted.Revision == nil || *deleted.Revision != 2 {
		t.Fatalf("UNRELATE output = %#v", deleted)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err = nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	dictionary = nativecatalog.NewService(nativecatalog.New(reopened), nativecatalog.ServiceOptions{})
	rows = NewService(New(reopened), dictionary, ServiceOptions{})
	afterDelete := executeMSQL(t, ctx, executor.New(dictionary, rows),
		"SHOW RELATIONS FROM work.notes FOR ROW 'row_source' DIRECTION OUTGOING LIMIT 10",
		executor.Parameters{}, executor.MutationOptions{},
	)
	if len(afterDelete.Rows) != 0 {
		t.Fatalf("relations after reopen = %#v", afterDelete.Rows)
	}
}

func executeMSQL(t *testing.T, ctx context.Context, engine *executor.Engine, source string, parameters executor.Parameters, options executor.MutationOptions) executor.Output {
	t.Helper()
	output, err := runMSQL(ctx, engine, source, parameters, options)
	if err != nil {
		t.Fatalf("Execute(%q) error = %v", source, err)
	}
	return output
}

func runMSQL(ctx context.Context, engine *executor.Engine, source string, parameters executor.Parameters, options executor.MutationOptions) (executor.Output, error) {
	document, err := parser.Parse(source)
	if err != nil {
		return executor.Output{}, err
	}
	return engine.Execute(ctx, document.Statement, parameters, options)
}

type testIDs struct {
	values []string
	index  int
}

func (source *testIDs) Next() (string, error) {
	value := source.values[source.index]
	source.index++
	return value, nil
}

type testClock struct{ value time.Time }

func (clock testClock) Now() time.Time { return clock.value }
