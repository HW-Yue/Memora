package nativesnapshot_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/nativesnapshot"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/snapshot"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestLogicalKVAndTypedNativeFixedMSQLCorpusHaveParity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, 7, 30, 20, 0, 0, 0, time.UTC)
	logicalPath := filepath.Join(t.TempDir(), "shadow.db")
	nativePath := filepath.Join(t.TempDir(), "shadow.memora")
	logical, err := nativekvstore.Open(logicalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logical.Close() })
	logicalCatalog := catalog.New(logical, catalog.Options{IDs: &parityIDs{values: []string{"database", "table", "title"}}, Clock: parityClock{now}})
	logicalRows := row.New(logical, logicalCatalog, row.Options{IDs: &parityIDs{values: []string{"first", "second"}}, Clock: parityClock{now}, RelationIDs: &parityIDs{values: []string{"edge"}}})

	nativeFile, err := nativestore.Create(nativePath, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nativeFile.Close() })
	nativeCatalog := nativecatalog.NewService(nativecatalog.New(nativeFile), nativecatalog.ServiceOptions{IDs: &parityIDs{values: []string{"database", "table", "title"}}, Clock: parityClock{now}})
	nativeRows := nativerow.NewService(nativerow.New(nativeFile), nativeCatalog, nativerow.ServiceOptions{IDs: &parityIDs{values: []string{"first", "second", "edge"}}, Clock: parityClock{now}})

	logicalEngine, nativeEngine := executor.New(logicalCatalog, logicalRows), executor.New(nativeCatalog, nativeRows)
	steps := []parityStep{
		{source: "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'"},
		{source: "CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(40) NOT NULL PURPOSE 'Title')"},
		{source: "INSERT INTO work.notes (title) VALUES (:title)", params: executor.Parameters{Named: map[string]any{"title": "alpha"}}, options: parityMutation(1, 0)},
		{source: "INSERT INTO work.notes (title) VALUES (:title)", params: executor.Parameters{Named: map[string]any{"title": "beta"}}, options: parityMutation(1, 0)},
		{source: "UPDATE work.notes SET title = :title WHERE row_id = :row", params: executor.Parameters{Named: map[string]any{"title": "revised", "row": "row_first"}}, options: parityMutation(1, 1)},
		{source: "RELATE work.notes ROW :source TO work.notes ROW :target TYPE :type DESCRIPTION :description", params: executor.Parameters{Named: map[string]any{"source": "row_first", "target": "row_second", "type": "supports", "description": "explicit semantic edge"}}, options: executor.MutationOptions{MaxAffectedRows: 1}},
		{source: "SHOW DATABASES COMPACT"},
		{source: "SHOW TABLES FROM work COMPACT"},
		{source: "DESCRIBE TABLE work.notes COMPACT"},
		{source: "SELECT * FROM work.notes WHERE row_id = :row LIMIT 1", params: executor.Parameters{Named: map[string]any{"row": "row_first"}}},
		{source: "SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 10", params: executor.Parameters{Named: map[string]any{"row": "row_first"}}},
		{source: "SHOW RELATIONS FROM work.notes FOR ROW :row DIRECTION OUTGOING LIMIT 10", params: executor.Parameters{Named: map[string]any{"row": "row_first"}}},
	}
	for _, step := range steps {
		logicalOutput, err := executeParity(ctx, logicalEngine, step)
		if err != nil {
			t.Fatalf("logical KV %q: %v", step.source, err)
		}
		nativeOutput, err := executeParity(ctx, nativeEngine, step)
		if err != nil {
			t.Fatalf("native %q: %v", step.source, err)
		}
		if !reflect.DeepEqual(nativeOutput, logicalOutput) {
			t.Fatalf("%q envelope differs\nlogical KV: %#v\nnative: %#v", step.source, logicalOutput, nativeOutput)
		}
	}
	if err := logical.Close(); err != nil {
		t.Fatal(err)
	}
	if err := nativeFile.Close(); err != nil {
		t.Fatal(err)
	}
	logical, err = nativekvstore.Open(logicalPath)
	if err != nil {
		t.Fatal(err)
	}
	nativeFile, err = nativestore.Open(nativePath)
	if err != nil {
		t.Fatal(err)
	}
	logicalCatalog = catalog.New(logical, catalog.Options{})
	logicalRows = row.New(logical, logicalCatalog, row.Options{})
	nativeCatalog = nativecatalog.NewService(nativecatalog.New(nativeFile), nativecatalog.ServiceOptions{})
	nativeRows = nativerow.NewService(nativerow.New(nativeFile), nativeCatalog, nativerow.ServiceOptions{})
	logicalEngine, nativeEngine = executor.New(logicalCatalog, logicalRows), executor.New(nativeCatalog, nativeRows)
	for _, step := range steps[6:] {
		logicalOutput, err := executeParity(ctx, logicalEngine, step)
		if err != nil {
			t.Fatal(err)
		}
		nativeOutput, err := executeParity(ctx, nativeEngine, step)
		if err != nil || !reflect.DeepEqual(nativeOutput, logicalOutput) {
			t.Fatalf("restart parity for %q = %#v, %v, want %#v", step.source, nativeOutput, err, logicalOutput)
		}
	}
	logicalSnapshot, err := snapshot.New(logical).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nativeSnapshot, err := nativesnapshot.NewNative(nativeFile).Export()
	if err != nil {
		t.Fatal(err)
	}
	logicalHash, _ := snapshot.CanonicalHash(logicalSnapshot)
	nativeHash, _ := snapshot.CanonicalHash(nativeSnapshot)
	if nativeHash != logicalHash {
		t.Fatalf("snapshot hash differs: logical KV %s, native %s", logicalHash, nativeHash)
	}
}

type parityStep struct {
	source  string
	params  executor.Parameters
	options executor.MutationOptions
}

func parityMutation(schema, revision uint64) executor.MutationOptions {
	return executor.MutationOptions{ExpectedSchemaVersion: schema, ExpectedRevision: revision, MaxAffectedRows: 1, Actor: "agent:parity", Source: "fixed-corpus", Reason: "shadow validation"}
}

func executeParity(ctx context.Context, engine *executor.Engine, step parityStep) (executor.Output, error) {
	document, err := parser.Parse(step.source)
	if err != nil {
		return executor.Output{}, err
	}
	return engine.Execute(ctx, document.Statement, step.params, step.options)
}

type parityIDs struct {
	values []string
	index  int
}

func (source *parityIDs) Next() (string, error) {
	value := source.values[source.index]
	source.index++
	return value, nil
}

type parityClock struct{ value time.Time }

func (clock parityClock) Now() time.Time { return clock.value }
