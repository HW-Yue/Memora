package executor_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestScopedMSQLRejectsQualifiedDatabaseOutsideAuthorization(t *testing.T) {
	t.Parallel()

	document, err := parser.Parse(`SELECT title FROM private.notes LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	ctx := security.WithAuthorization(context.Background(), security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:host",
		AuthorizedDatabases: []string{"work"},
	})
	_, err = executor.New(nil, nil).Execute(
		ctx, document.Statement, executor.Parameters{}, executor.MutationOptions{},
	)
	if code(err) != "permission_denied" {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestScopedMSQLAcceptsStableDatabaseID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	subject, _, closeStore := queryFixture(t, ctx)
	defer closeStore()
	document, err := parser.Parse(`SELECT title FROM work.notes LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	scoped := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:host",
		AuthorizedDatabases: []string{"db_database"},
	})
	if _, err := subject.Execute(
		scoped, document.Statement, executor.Parameters{}, executor.MutationOptions{},
	); err != nil {
		t.Fatalf("Execute() with stable Database ID error = %v", err)
	}
}

func TestScopedCatalogDiscoveryAndRelationsDoNotLeakOtherDatabases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
	for _, name := range []string{"work", "private"} {
		if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
			Name: name, Purpose: name, Scope: name,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := dictionary.CreateTable(ctx, name, catalog.TableDefinition{
			Name: "notes", Purpose: "Notes", RowSemantics: "One note",
			Columns: []catalog.ColumnDefinition{{
				Name: "title", Type: "TEXT(100)", Purpose: "Title",
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows := row.New(databaseStore, dictionary, row.Options{
		RelationPolicy: row.RelationPolicyFunc(func(
			_ context.Context, _, _ catalog.Table,
		) bool {
			return true
		}),
	})
	subject := executor.New(dictionary, rows)
	source, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "source"}, row.WriteOptions{
		ExpectedSchemaVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := rows.Insert(ctx, "private", "notes", map[string]any{"title": "target"}, row.WriteOptions{
		ExpectedSchemaVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := execute(ctx, subject,
		"RELATE work.notes ROW :source TO private.notes ROW :target TYPE 'links'",
		executor.Parameters{Named: map[string]any{"source": source.ID, "target": target.ID}},
		executor.MutationOptions{MaxAffectedRows: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	relationID, _ := created.Rows[0]["relation_id"].(string)
	scoped := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:host",
		AuthorizedDatabases: []string{"work"},
	})
	shown, err := execute(scoped, subject, "SHOW DATABASES", executor.Parameters{}, executor.MutationOptions{})
	if err != nil || len(shown.Rows) != 1 || shown.Rows[0]["name"] != "work" {
		t.Fatalf("scoped SHOW DATABASES = %#v, %v", shown, err)
	}
	_, err = execute(scoped, subject,
		"SHOW RELATIONS FROM work.notes FOR ROW :row DIRECTION OUTGOING LIMIT 10",
		executor.Parameters{Named: map[string]any{"row": source.ID}},
		executor.MutationOptions{},
	)
	if code(err) != "permission_denied" {
		t.Fatalf("cross-scope SHOW RELATIONS error = %v", err)
	}
	_, err = execute(scoped, subject, "UNRELATE :relation", executor.Parameters{
		Named: map[string]any{"relation": relationID},
	}, executor.MutationOptions{ExpectedRevision: 1, MaxAffectedRows: 1})
	if code(err) != "permission_denied" {
		t.Fatalf("cross-scope UNRELATE error = %v", err)
	}
	if current, err := rows.GetRelation(ctx, relationID); err != nil || current.ID != relationID {
		t.Fatalf("relation after denied UNRELATE = %#v, %v", current, err)
	}
}
