package row_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/row"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestMatchUsesConsistentRealAgentAndMechanicalPostings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "body"}},
	})
	createSchema(t, ctx, dictionary)
	service := row.New(databaseStore, dictionary, row.Options{
		IDs: &idSource{values: []string{"semantic", "literal"}},
	})
	semantic, err := service.Insert(ctx, "work", "notes", map[string]any{
		"title": "unrelated wording",
	}, row.WriteOptions{ExpectedSchemaVersion: 1, IndexTerms: []string{"architecture"}})
	if err != nil {
		t.Fatal(err)
	}
	literal, err := service.Insert(ctx, "work", "notes", map[string]any{
		"title": "architecture literal",
	}, row.WriteOptions{ExpectedSchemaVersion: 1, IndexTerms: []string{"other"}})
	if err != nil {
		t.Fatal(err)
	}
	resultValue, err := service.Match(
		ctx, semantic.DatabaseID, semantic.TableID,
		"architecture", []string{"architecture"}, 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(resultValue.Matches) != 2 ||
		resultValue.Matches[0].RowID != semantic.ID ||
		resultValue.Matches[0].Score != 0.8 ||
		resultValue.Matches[1].RowID != literal.ID ||
		resultValue.Matches[1].Score != 0.2 {
		t.Fatalf("Match() = %#v", resultValue)
	}
}
