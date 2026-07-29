package binder_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/binder"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestCatalogBinderExecutesDiscoveryAndDDLByStableIdentity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "body", "status"}},
	})
	subject := binder.NewCatalog(service)

	execute(t, ctx, subject, "CREATE DATABASE work PURPOSE 'Project knowledge' SCOPE 'Active projects'")
	execute(t, ctx, subject, "CREATE TABLE work.notes PURPOSE 'Durable notes' ROW SEMANTICS 'One reviewed note' (title TEXT NOT NULL PURPOSE 'Display title', body TEXT(1200) PURPOSE 'Complete note')")

	databases := execute(t, ctx, subject, "SHOW DATABASES")
	if len(databases.Databases) != 1 || databases.Databases[0].ID != "db_database" {
		t.Fatalf("SHOW DATABASES = %#v", databases)
	}
	if len(databases.Databases[0].Tables) != 0 {
		t.Fatalf("SHOW DATABASES expanded nested tables: %#v", databases)
	}
	tables := execute(t, ctx, subject, "SHOW TABLES FROM work")
	if len(tables.Tables) != 1 || tables.Tables[0].ID != "tbl_table" {
		t.Fatalf("SHOW TABLES = %#v", tables)
	}
	if len(tables.Tables[0].Columns) != 0 {
		t.Fatalf("SHOW TABLES expanded nested columns: %#v", tables)
	}
	column := execute(t, ctx, subject, "DESCRIBE COLUMN work.notes.title COMPACT")
	if column.Column == nil || column.Column.ID != "col_title" || column.Column.Nullable {
		t.Fatalf("DESCRIBE COLUMN = %#v", column)
	}

	execute(t, ctx, subject, "ALTER TABLE work.notes ADD COLUMN status TEXT NULL PURPOSE 'Workflow state'")
	execute(t, ctx, subject, "ALTER TABLE work.notes RENAME COLUMN title TO heading")
	execute(t, ctx, subject, "ALTER TABLE work.notes RENAME TO knowledge")
	execute(t, ctx, subject, "ALTER DATABASE work RENAME TO projects")

	renamed := execute(t, ctx, subject, "DESCRIBE COLUMN work.notes.title")
	if renamed.Column == nil || renamed.Column.ID != "col_title" || renamed.Column.Name != "heading" {
		t.Fatalf("alias resolution = %#v", renamed)
	}
	columns := execute(t, ctx, subject, "SHOW COLUMNS FROM projects.knowledge")
	if len(columns.Columns) != 3 {
		t.Fatalf("SHOW COLUMNS = %#v", columns)
	}
	database := execute(t, ctx, subject, "DESCRIBE DATABASE projects COMPACT")
	if database.Database == nil || database.Database.SchemaVersion != 6 {
		t.Fatalf("DESCRIBE DATABASE = %#v", database)
	}
	if len(database.Database.Tables) != 0 {
		t.Fatalf("compact database expanded nested tables: %#v", database)
	}
}

func TestCatalogBinderRejectsMissingMetadataQualificationAndDuplicates(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	subject := binder.NewCatalog(catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table"}},
	}))

	_, err = bind(ctx, subject, "CREATE DATABASE work")
	assertBinderCode(t, err, "validation_error")
	execute(t, ctx, subject, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'")
	_, err = bind(ctx, subject, "CREATE DATABASE WORK PURPOSE 'Duplicate' SCOPE 'Anything'")
	assertBinderCode(t, err, "already_exists")
	_, err = bind(ctx, subject, "CREATE TABLE notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (body TEXT PURPOSE 'Body')")
	assertBinderCode(t, err, "validation_error")
	_, err = bind(ctx, subject, "SHOW TABLES")
	assertBinderCode(t, err, "validation_error")
	_, err = bind(ctx, subject, "DESCRIBE COLUMN work.notes")
	assertBinderCode(t, err, "validation_error")
}

func TestCatalogBinderNormalizesContextAndStoreFailures(t *testing.T) {
	t.Parallel()

	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	subject := binder.NewCatalog(catalog.New(databaseStore, catalog.Options{}))

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = bind(cancelled, subject, "SHOW DATABASES")
	assertBinderCode(t, err, "cancelled")

	if err := databaseStore.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = bind(context.Background(), subject, "SHOW DATABASES")
	assertBinderCode(t, err, "internal_error")
}

func execute(t *testing.T, ctx context.Context, subject *binder.Catalog, source string) binder.CatalogResult {
	t.Helper()
	result, err := bind(ctx, subject, source)
	if err != nil {
		t.Fatalf("%s: %v", source, err)
	}
	return result
}

func bind(ctx context.Context, subject *binder.Catalog, source string) (binder.CatalogResult, error) {
	document, err := parser.Parse(source)
	if err != nil {
		return binder.CatalogResult{}, err
	}
	return subject.Execute(ctx, document.Statement)
}

func assertBinderCode(t *testing.T, err error, want string) {
	t.Helper()
	var bindError interface{ StableCode() string }
	if !errors.As(err, &bindError) || bindError.StableCode() != want {
		t.Fatalf("error = %v, want stable code %q", err, want)
	}
}

type idSource struct {
	values []string
}

func (source *idSource) Next() (string, error) {
	value := source.values[0]
	source.values = source.values[1:]
	return value, nil
}
