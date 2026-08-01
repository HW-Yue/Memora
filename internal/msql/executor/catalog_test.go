package executor_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestCatalogMetadataReadPaginatesOneSnapshotAndRejectsInvalidContinuation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
	for _, name := range []string{"gamma", "alpha", "beta"} {
		if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
			Name: name, Purpose: name, Scope: name,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tableName := range []string{"zeta", "notes", "archive"} {
		columns := []catalog.ColumnDefinition{{Name: "body", Type: "TEXT", Purpose: "Body"}}
		if tableName == "notes" {
			columns = append(columns,
				catalog.ColumnDefinition{Name: "title", Type: "TEXT", Purpose: "Title"},
				catalog.ColumnDefinition{Name: "status", Type: "TEXT", Purpose: "Status"},
			)
		}
		if _, err := dictionary.CreateTable(ctx, "alpha", catalog.TableDefinition{
			Name: tableName, Purpose: tableName, RowSemantics: "One " + tableName, Columns: columns,
		}); err != nil {
			t.Fatal(err)
		}
	}
	subject := executor.New(dictionary, row.New(databaseStore, dictionary, row.Options{}))
	first, err := execute(ctx, subject, "SHOW DATABASES LIMIT :limit", executor.Parameters{
		Named: map[string]any{"limit": int64(2)},
	}, executor.MutationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Page == nil || first.Page.Version != result.ListPageVersion ||
		first.Page.Limit != 2 || first.Page.Snapshot == "" || !first.Truncated ||
		first.NextCursor == "" || len(first.Rows) != 2 || first.Rows[0]["name"] != "alpha" ||
		first.Rows[1]["name"] != "beta" {
		t.Fatalf("first metadata page = %#v", first)
	}

	second, err := execute(ctx, subject,
		"SHOW DATABASES CURSOR :cursor LIMIT :limit",
		executor.Parameters{Named: map[string]any{
			"cursor": first.NextCursor, "limit": int64(2),
		}}, executor.MutationOptions{})
	if err != nil || second.Page == nil || second.Page.Cursor != first.NextCursor ||
		second.Page.Snapshot != first.Page.Snapshot || second.Truncated ||
		len(second.Rows) != 1 || second.Rows[0]["name"] != "gamma" {
		t.Fatalf("second metadata page = %#v, %v", second, err)
	}
	tables, err := execute(ctx, subject, "SHOW TABLES FROM alpha LIMIT 2", executor.Parameters{}, executor.MutationOptions{})
	if err != nil || tables.Page == nil || !tables.Truncated || len(tables.Rows) != 2 {
		t.Fatalf("Table metadata page = %#v, %v", tables, err)
	}
	columns, err := execute(ctx, subject, "SHOW COLUMNS FROM alpha.notes LIMIT 2", executor.Parameters{}, executor.MutationOptions{})
	if err != nil || columns.Page == nil || !columns.Truncated || len(columns.Rows) != 2 {
		t.Fatalf("Column metadata page = %#v, %v", columns, err)
	}

	tampered := first.NextCursor[:len(first.NextCursor)-1] + "A"
	if strings.HasSuffix(first.NextCursor, "A") {
		tampered = first.NextCursor[:len(first.NextCursor)-1] + "B"
	}
	_, err = execute(ctx, subject, "SHOW DATABASES CURSOR :cursor LIMIT 2", executor.Parameters{
		Named: map[string]any{"cursor": tampered},
	}, executor.MutationOptions{})
	if code(err) != string(result.CodeValidation) {
		t.Fatalf("tampered cursor error = %v", err)
	}
	_, err = execute(ctx, subject, "SHOW TABLES FROM alpha CURSOR :cursor LIMIT 2", executor.Parameters{
		Named: map[string]any{"cursor": first.NextCursor},
	}, executor.MutationOptions{})
	if code(err) != string(result.CodeValidation) {
		t.Fatalf("cross-scope cursor error = %v", err)
	}

	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "delta", Purpose: "delta", Scope: "delta",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = execute(ctx, subject, "SHOW DATABASES CURSOR :cursor LIMIT 2", executor.Parameters{
		Named: map[string]any{"cursor": first.NextCursor},
	}, executor.MutationOptions{})
	if code(err) != string(result.CodeRevisionConflict) {
		t.Fatalf("changed snapshot error = %v", err)
	}
}

func TestCatalogMetadataReadAppliesDefaultAndMaximumLimit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
	for number := 0; number < 65; number++ {
		name := fmt.Sprintf("database_%03d", number)
		if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
			Name: name, Purpose: name, Scope: name,
		}); err != nil {
			t.Fatal(err)
		}
	}
	subject := executor.New(dictionary, row.New(databaseStore, dictionary, row.Options{}))
	output, err := execute(ctx, subject, "SHOW DATABASES", executor.Parameters{}, executor.MutationOptions{})
	if err != nil || output.Page == nil || output.Page.Limit != 64 ||
		len(output.Rows) != 64 || !output.Truncated || output.NextCursor == "" {
		t.Fatalf("default metadata page = %#v, %v", output, err)
	}
	for _, source := range []string{
		"SHOW DATABASES LIMIT 0",
		"SHOW DATABASES LIMIT 257",
		"SHOW DATABASES CURSOR 7 LIMIT 1",
	} {
		if _, err := execute(ctx, subject, source, executor.Parameters{}, executor.MutationOptions{}); code(err) != string(result.CodeValidation) {
			t.Fatalf("%s error = %v", source, err)
		}
	}
}

func TestBatchSessionExecutesCatalogBinderWithoutADDLBypass(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
	session := executor.NewBatchSession(
		ctx, dictionary, row.New(databaseStore, dictionary, row.Options{}),
	)
	defer session.Close()
	created := session.Execute(ctx, executor.BatchRequest{
		RequestID: "catalog-create",
		Source: "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'; " +
			"CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' " +
			"(title TEXT NOT NULL PURPOSE 'Title'); SHOW TABLES FROM work LIMIT 1",
	})
	assertStatuses(t, created,
		result.StatusSucceeded, result.StatusSucceeded, result.StatusSucceeded,
	)
	if len(created.Results[0].Rows) != 1 ||
		created.Results[0].Rows[0]["database_id"] == "" ||
		len(created.Results[2].Rows) != 1 ||
		created.Results[2].Rows[0]["table_id"] == "" {
		t.Fatalf("Catalog results = %#v", created.Results)
	}
	if created.Results[2].Page == nil || created.Results[2].Page.Version != result.ListPageVersion ||
		created.Results[2].Page.Limit != 1 {
		t.Fatalf("Catalog result page = %#v", created.Results[2].Page)
	}
	duplicate := session.Execute(ctx, executor.BatchRequest{
		RequestID: "catalog-duplicate",
		Source:    "CREATE DATABASE WORK PURPOSE 'Duplicate' SCOPE 'Anything'",
	})
	if duplicate.OK || duplicate.Results[0].Error == nil ||
		duplicate.Results[0].Error.Code != result.CodeAlreadyExists {
		t.Fatalf("duplicate Catalog result = %#v", duplicate)
	}
}
