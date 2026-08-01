package executor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestCatalogAtlasPagesEveryAuthorizedDatabaseAndColdTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databaseStore.Close() })
	dictionary := catalog.New(databaseStore, catalog.Options{})
	for number := 0; number < 7; number++ {
		name := fmt.Sprintf("database_%02d", number)
		if number == 6 {
			name = "zeta_cold"
		}
		if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: name, Purpose: "Knowledge for " + name, Scope: name}); err != nil {
			t.Fatal(err)
		}
		if _, err := dictionary.CreateTable(ctx, name, catalog.TableDefinition{Name: "notes", Purpose: "Notes in " + name,
			RowSemantics: "One complete note", Columns: []catalog.ColumnDefinition{{Name: "body", Type: "TEXT", Purpose: "Body"}}}); err != nil {
			t.Fatal(err)
		}
	}
	subject := executor.New(dictionary, row.New(databaseStore, dictionary, row.Options{}))
	parameters := executor.Parameters{Named: map[string]any{"limit": int64(3), "bytes": int64(4096)}}
	source := "SHOW CATALOG ATLAS LIMIT :limit BYTES :bytes COMPACT"
	seenCold, entries, cursor, snapshot := false, 0, "", ""
	for {
		if cursor != "" {
			parameters.Named["cursor"] = cursor
			source = "SHOW CATALOG ATLAS CURSOR :cursor LIMIT :limit BYTES :bytes COMPACT"
		}
		output, executeErr := execute(ctx, subject, source, parameters, executor.MutationOptions{})
		if executeErr != nil {
			t.Fatal(executeErr)
		}
		if output.Page == nil || len(output.Rows) == 0 || len(output.Rows) > 3 {
			t.Fatalf("Atlas page = %#v", output)
		}
		if snapshot == "" {
			snapshot = output.Page.Snapshot
		} else if output.Page.Snapshot != snapshot {
			t.Fatalf("Atlas snapshot drift = %q, want %q", output.Page.Snapshot, snapshot)
		}
		for _, entry := range output.Rows {
			entries++
			if entry["database"] == "zeta_cold" && entry["kind"] == "table" && entry["table"] == "notes" {
				seenCold = true
			}
			if _, leaked := entry["columns"]; leaked {
				t.Fatalf("Atlas leaked Columns = %#v", entry)
			}
		}
		if !output.Truncated {
			break
		}
		if output.NextCursor == "" {
			t.Fatal("truncated Atlas omitted continuation")
		}
		cursor = output.NextCursor
	}
	if entries != 14 || !seenCold {
		t.Fatalf("Atlas coverage entries=%d cold=%t", entries, seenCold)
	}
}

func TestCatalogAtlasAppliesAuthorizationBeforeSnapshotAndRejectsDrift(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databaseStore.Close() })
	dictionary := catalog.New(databaseStore, catalog.Options{})
	for _, name := range []string{"allowed", "secret"} {
		if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: name, Purpose: name, Scope: name}); err != nil {
			t.Fatal(err)
		}
		if _, err := dictionary.CreateTable(ctx, name, catalog.TableDefinition{Name: "notes", Purpose: name,
			RowSemantics: "One note", Columns: []catalog.ColumnDefinition{{Name: "body", Type: "TEXT", Purpose: "Body"}}}); err != nil {
			t.Fatal(err)
		}
	}
	subject := executor.New(dictionary, row.New(databaseStore, dictionary, row.Options{}))
	authorized := security.WithAuthorization(ctx, security.Authorization{Version: security.AuthorizationVersion,
		Actor: "agent:test", AuthorizedDatabases: []string{"allowed"}})
	first, err := execute(authorized, subject, "SHOW CATALOG ATLAS LIMIT 1 BYTES 4096 COMPACT", executor.Parameters{}, executor.MutationOptions{})
	if err != nil || first.Page == nil || !first.Truncated || len(first.Rows) != 1 || first.Rows[0]["database"] != "allowed" {
		t.Fatalf("authorized Atlas = %#v, %v", first, err)
	}
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "secret_new", Purpose: "secret", Scope: "secret"}); err != nil {
		t.Fatal(err)
	}
	tampered := first.NextCursor[:len(first.NextCursor)-1] + "A"
	if strings.HasSuffix(first.NextCursor, "A") {
		tampered = first.NextCursor[:len(first.NextCursor)-1] + "B"
	}
	if _, err := execute(authorized, subject, "SHOW CATALOG ATLAS CURSOR :cursor LIMIT 8 BYTES 4096 COMPACT",
		executor.Parameters{Named: map[string]any{"cursor": tampered}}, executor.MutationOptions{}); code(err) != string(result.CodeValidation) {
		t.Fatalf("tampered Atlas cursor error = %v", err)
	}
	second, err := execute(authorized, subject, "SHOW CATALOG ATLAS CURSOR :cursor LIMIT 8 BYTES 4096 COMPACT",
		executor.Parameters{Named: map[string]any{"cursor": first.NextCursor}}, executor.MutationOptions{})
	if err != nil || len(second.Rows) != 1 || second.Rows[0]["kind"] != "table" || second.Rows[0]["database"] != "allowed" {
		t.Fatalf("authorized continuation = %#v, %v", second, err)
	}
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "allowed_new", Purpose: "new", Scope: "new"}); err != nil {
		t.Fatal(err)
	}
	driftScope := security.WithAuthorization(ctx, security.Authorization{Version: security.AuthorizationVersion,
		Actor: "agent:test", AuthorizedDatabases: []string{"allowed", "allowed_new"}})
	_, err = execute(driftScope, subject, "SHOW CATALOG ATLAS CURSOR :cursor LIMIT 8 BYTES 4096 COMPACT",
		executor.Parameters{Named: map[string]any{"cursor": first.NextCursor}}, executor.MutationOptions{})
	if code(err) != string(result.CodeRevisionConflict) {
		t.Fatalf("Atlas drift error = %v", err)
	}
}

func TestCatalogAtlasEnforcesExactRowJSONByteBudgetAndBounds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databaseStore.Close() })
	dictionary := catalog.New(databaseStore, catalog.Options{})
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: name,
			Purpose: "A bounded but descriptive semantic purpose for " + name, Scope: "knowledge"}); err != nil {
			t.Fatal(err)
		}
	}
	subject := executor.New(dictionary, row.New(databaseStore, dictionary, row.Options{}))
	output, err := execute(ctx, subject, "SHOW CATALOG ATLAS LIMIT 64 BYTES 512 COMPACT", executor.Parameters{}, executor.MutationOptions{})
	if err != nil || output.Page == nil || !output.Truncated || output.NextCursor == "" {
		t.Fatalf("byte-bounded Atlas = %#v, %v", output, err)
	}
	encoded, err := json.Marshal(output.Rows)
	if err != nil || len(encoded) > 512 {
		t.Fatalf("Atlas row JSON bytes = %d, %v", len(encoded), err)
	}
	for _, source := range []string{
		"SHOW CATALOG ATLAS LIMIT 1 BYTES 511 COMPACT",
		"SHOW CATALOG ATLAS LIMIT 257 BYTES 4096 COMPACT",
	} {
		if _, err := execute(ctx, subject, source, executor.Parameters{}, executor.MutationOptions{}); code(err) != string(result.CodeValidation) {
			t.Fatalf("%s error = %v", source, err)
		}
	}
}
