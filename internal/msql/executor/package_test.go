package executor_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/dbpackage"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestPackageOperationsRunThroughMSQLExecutor(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sourceStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sourceStore.Close()
	sourceCatalog := catalog.New(sourceStore, catalog.Options{IDs: &idSource{values: []string{"work", "notes", "body"}}})
	if _, err := sourceCatalog.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "portable", Scope: "reviewed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceCatalog.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "notes", RowSemantics: "one note",
		Columns: []catalog.ColumnDefinition{{Name: "body", Type: "TEXT", Purpose: "body"}},
	}); err != nil {
		t.Fatal(err)
	}
	sourceRows := row.New(sourceStore, sourceCatalog, row.Options{IDs: &idSource{values: []string{"note"}}})
	if _, err := sourceRows.Insert(ctx, "work", "notes", map[string]any{"body": "portable"}, row.WriteOptions{ExpectedSchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	sourceEngine := executor.NewWithPackages(sourceCatalog, sourceRows, dbpackage.New(sourceStore))
	packCtx := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "user:test",
		AuthorizedDatabases: []string{"work"},
	})
	packed := executePackage(t, packCtx, sourceEngine, `PACK DATABASE work BY :author`, executor.Parameters{Named: map[string]any{"author": "Alice"}})
	if len(packed.Rows) != 1 {
		t.Fatalf("PACK output = %#v", packed)
	}
	encoded, ok := packed.Rows[0]["package"].(string)
	if !ok || encoded == "" || packed.Rows[0]["package_sha256"] != dbpackage.Hash([]byte(encoded)) {
		t.Fatalf("PACK row = %#v", packed.Rows[0])
	}

	targetStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "target.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer targetStore.Close()
	targetCatalog := catalog.New(targetStore, catalog.Options{})
	targetRows := row.New(targetStore, targetCatalog, row.Options{})
	targetEngine := executor.NewWithPackages(targetCatalog, targetRows, dbpackage.New(targetStore))
	opened := executePackage(t, ctx, targetEngine, `OPEN PACKAGE :package READ ONLY`, executor.Parameters{Named: map[string]any{"package": encoded}})
	if len(opened.Rows) != 1 || opened.Rows[0]["read_only"] != true || opened.Rows[0]["name"] != "work" {
		t.Fatalf("OPEN output = %#v", opened)
	}
	if databases, err := targetCatalog.ShowDatabases(ctx); err != nil || len(databases) != 0 {
		t.Fatalf("OPEN mutated target: %#v, %v", databases, err)
	}
	installCtx := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "user:test",
		AuthorizedDatabases: []string{"work"}, DefaultLevel: security.LevelStructural,
		Approval: &security.Approval{
			Version: security.ApprovalVersion, Action: security.ActionInstallPackage,
			SubjectSHA256: dbpackage.Hash([]byte(encoded)), Confirmed: true,
		},
	})
	installed := executePackage(t, installCtx, targetEngine, `INSTALL PACKAGE ? TRUSTED`, executor.Parameters{Positional: []any{encoded}})
	if installed.AffectedRows != 1 || len(installed.Rows) != 1 || installed.Rows[0]["name"] != "work" || installed.Rows[0]["read_only"] != true {
		t.Fatalf("INSTALL output = %#v", installed)
	}
	if stored, err := targetRows.Get(ctx, "work", "notes", "row_note"); err != nil || stored.Values["body"] != "portable" {
		t.Fatalf("installed row = %#v, %v", stored, err)
	}
	_, err = execute(writePackageContext(ctx), targetEngine,
		`INSERT INTO work.notes (body) VALUES ('must fail')`, executor.Parameters{},
		executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1})
	if code(err) != "permission_denied" {
		t.Fatalf("write to installed package error = %v", err)
	}
}

func writePackageContext(ctx context.Context) context.Context {
	return security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:test",
		AuthorizedDatabases: []string{"work"}, DefaultLevel: security.LevelWrite,
	})
}

func executePackage(t *testing.T, ctx context.Context, engine *executor.Engine, source string, parameters executor.Parameters) executor.Output {
	t.Helper()
	document, err := parser.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	output, err := engine.Execute(ctx, document.Statement, parameters, executor.MutationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return output
}
