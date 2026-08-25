package executor_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/lexicallocation"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

type fakeLexicalLocations struct {
	requests []lexicallocation.Request
	page     lexicallocation.Page
	err      error
}

func (fake *fakeLexicalLocations) SearchLexicalLocations(
	_ context.Context, request lexicallocation.Request,
) (lexicallocation.Page, error) {
	request.DatabaseIDs = append([]string(nil), request.DatabaseIDs...)
	fake.requests = append(fake.requests, request)
	return fake.page, fake.err
}

func TestLexicalLocationsAppliesCatalogAuthorizationBeforeNarrowRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	dictionary := catalog.New(store, catalog.Options{})
	allowed, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "allowed", Purpose: "Allowed", Scope: "Allowed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "secret", Purpose: "Secret", Scope: "Secret"}); err != nil {
		t.Fatal(err)
	}
	reader := &fakeLexicalLocations{page: lexicallocation.Page{Version: lexicallocation.Version,
		Locations: []lexicallocation.Location{{Kind: fulltext.KindRow, DatabaseID: allowed.ID,
			TableID: "tbl_notes", ObjectID: "row_1", Revision: 3, MatchedTermCount: 2,
			MatchedFieldCount: 1, Frequency: 4, MatchedFieldIDs: []string{"col_body"}}},
		Limit: 8, Snapshot: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	engine := executor.NewWithLexicalLocations(dictionary, row.New(store, dictionary, row.Options{}), reader)
	authorized := security.WithAuthorization(ctx, security.Authorization{Version: security.AuthorizationVersion,
		Actor: "agent:test", AuthorizedDatabases: []string{"allowed"}, DefaultLevel: security.LevelRead})
	output, err := execute(authorized, engine,
		"SHOW LEXICAL LOCATIONS FROM ALL TABLES USING :query LIMIT :limit BYTES :bytes",
		executor.Parameters{Named: map[string]any{"query": "recovery plan", "limit": int64(8), "bytes": int64(4096)}},
		executor.MutationOptions{})
	if err != nil || len(reader.requests) != 1 || !reflect.DeepEqual(reader.requests[0].DatabaseIDs, []string{allowed.ID}) {
		t.Fatalf("scoped lexical output=%#v requests=%#v error=%v", output, reader.requests, err)
	}
	// A location is where the hit is, plus the identity of the hit itself. The
	// counts and matched-field list are gone: they are scores under other
	// names, and a caller given them ranks and filters by them.
	if len(output.Rows) != 1 || output.Rows[0]["kind"] != "row" || output.Rows[0]["object_id"] != "row_1" ||
		output.Page == nil || output.Page.Snapshot != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("lexical location output = %#v", output)
	}
	for _, gone := range []string{
		"revision", "matched_term_count", "matched_field_count", "frequency", "matched_field_ids",
	} {
		if _, exists := output.Rows[0][gone]; exists {
			t.Fatalf("lexical location still carries %q: %#v", gone, output.Rows[0])
		}
	}
}

func TestLexicalLocationsValidatesBudgetsPortAndCursor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	dictionary := catalog.New(store, catalog.Options{})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Work"}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(store, dictionary, row.Options{})
	for _, source := range []string{
		"SHOW LEXICAL LOCATIONS FROM ALL TABLES USING 'term' LIMIT 0 BYTES 4096",
		"SHOW LEXICAL LOCATIONS FROM ALL TABLES USING 'term' LIMIT 65 BYTES 4096",
		"SHOW LEXICAL LOCATIONS FROM ALL TABLES USING 'term' LIMIT 8 BYTES 255",
		"SHOW LEXICAL LOCATIONS FROM ALL TABLES USING 'term' LIMIT 8 BYTES 65537",
		"SHOW LEXICAL LOCATIONS FROM ALL TABLES USING 'term' CURSOR 7 LIMIT 8 BYTES 4096",
	} {
		if _, err := execute(ctx, executor.NewWithLexicalLocations(dictionary, rows, &fakeLexicalLocations{}), source,
			executor.Parameters{}, executor.MutationOptions{}); code(err) != string(result.CodeValidation) {
			t.Fatalf("%s error = %v", source, err)
		}
	}
	if _, err := execute(ctx, executor.New(dictionary, rows),
		"SHOW LEXICAL LOCATIONS FROM ALL TABLES USING 'term' LIMIT 8 BYTES 4096",
		executor.Parameters{}, executor.MutationOptions{}); code(err) != string(result.CodeUnsupported) {
		t.Fatalf("missing lexical port error = %v", err)
	}
}

func TestLexicalLocationsRejectsBackendScopeLeak(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	dictionary := catalog.New(store, catalog.Options{})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "allowed", Purpose: "Allowed", Scope: "Allowed"}); err != nil {
		t.Fatal(err)
	}
	reader := &fakeLexicalLocations{page: lexicallocation.Page{Version: lexicallocation.Version,
		Locations: []lexicallocation.Location{{Kind: fulltext.KindRow, DatabaseID: "db_secret", TableID: "tbl",
			ObjectID: "row", Revision: 1, MatchedTermCount: 1, MatchedFieldCount: 1,
			Frequency: 1, MatchedFieldIDs: []string{"body"}}}, Limit: 8,
		Snapshot: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	engine := executor.NewWithLexicalLocations(dictionary, row.New(store, dictionary, row.Options{}), reader)
	authorized := security.WithAuthorization(ctx, security.Authorization{Version: security.AuthorizationVersion,
		Actor: "agent:test", AuthorizedDatabases: []string{"allowed"}, DefaultLevel: security.LevelRead})
	_, err = execute(authorized, engine, "SHOW LEXICAL LOCATIONS FROM ALL TABLES USING 'term' LIMIT 8 BYTES 4096",
		executor.Parameters{}, executor.MutationOptions{})
	if code(err) != string(result.CodeInternal) {
		t.Fatalf("scope leak error = %v", err)
	}
}
