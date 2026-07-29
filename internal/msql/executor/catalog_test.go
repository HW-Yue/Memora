package executor_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestBatchSessionExecutesCatalogBinderWithoutADDLBypass(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
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
			"(title TEXT NOT NULL PURPOSE 'Title'); SHOW TABLES FROM work",
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
	duplicate := session.Execute(ctx, executor.BatchRequest{
		RequestID: "catalog-duplicate",
		Source:    "CREATE DATABASE WORK PURPOSE 'Duplicate' SCOPE 'Anything'",
	})
	if duplicate.OK || duplicate.Results[0].Error == nil ||
		duplicate.Results[0].Error.Code != result.CodeAlreadyExists {
		t.Fatalf("duplicate Catalog result = %#v", duplicate)
	}
}
