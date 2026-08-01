package nativerow

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/discovery"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestLexicalRouteCandidatesEnforcesAuthorizationBudgetAndZeroHitFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs:   &testIDs{values: []string{"work", "notes", "title", "private", "secrets", "secret_title"}},
		Clock: testClock{value: now},
	})
	rows := NewService(New(file), dictionary, ServiceOptions{
		IDs:   &testIDs{values: []string{"work_root", "work_recovery", "private_root", "private_recovery"}},
		Clock: testClock{value: now},
	})
	engine := executor.New(dictionary, rows)
	executeMSQL(t, ctx, engine, "CREATE DATABASE work PURPOSE 'Work knowledge' SCOPE 'Engineering'", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE TABLE work.notes PURPOSE 'Recovery notes' ROW SEMANTICS 'One decision' (title TEXT(80) NOT NULL PURPOSE 'Title')", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'", executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
	executeMSQL(t, ctx, engine, "CREATE ROUTE UNDER 'route_work_root' NAME 'crash-recovery' KIND 'leaf' PURPOSE 'Recovery procedures'", executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
	executeMSQL(t, ctx, engine, "CREATE DATABASE private PURPOSE 'Private recovery' SCOPE 'Secrets'", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE TABLE private.secrets PURPOSE 'Recovery secrets' ROW SEMANTICS 'One secret' (title TEXT(80) NOT NULL PURPOSE 'Title')", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE ROUTE ROOT FOR TABLE private.secrets PURPOSE 'Secret Router'", executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
	executeMSQL(t, ctx, engine, "CREATE ROUTE UNDER 'route_private_root' NAME 'secret-recovery' KIND 'leaf' PURPOSE 'Private recovery procedures'", executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})

	authorized := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:test", AuthorizedDatabases: []string{"work"},
	})
	parameters := executor.Parameters{Named: map[string]any{
		"query": "recovery", "limit": int64(1), "bytes": int64(4096),
	}}
	output, err := runMSQL(authorized, engine,
		"SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL :query LIMIT :limit BYTES :bytes",
		parameters, executor.MutationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Rows) != 0 || output.Discovery == nil || !output.Truncated ||
		output.Discovery.Usage != discovery.UsageNavigationOnly || len(output.Discovery.Candidates) != 1 {
		t.Fatalf("bounded lexical discovery = %#v", output)
	}
	candidate := output.Discovery.Candidates[0]
	if candidate.DatabaseID != "db_work" || candidate.TableID != "tbl_notes" ||
		candidate.RouteID != "route_work_recovery" || candidate.Predictor != "lexical-route/v1" ||
		candidate.ScoreKind != discovery.ScoreMatchCount || candidate.Score == nil ||
		strings.Contains(candidate.Reason, "recovery") {
		t.Fatalf("authorized candidate = %#v", candidate)
	}
	for _, value := range output.Discovery.Candidates {
		if value.DatabaseID == "db_private" {
			t.Fatalf("private candidate leaked: %#v", value)
		}
	}
	session := executor.NewBatchSession(authorized, dictionary, rows)
	defer session.Close()
	envelope := session.Execute(authorized, executor.BatchRequest{
		RequestID: "req-route-candidates",
		Source:    "SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL :query LIMIT :limit BYTES :bytes",
		Statements: []executor.StatementInput{{
			Parameters: parameters,
			Authorization: security.Authorization{
				Version: security.AuthorizationVersion, Actor: "agent:test", AuthorizedDatabases: []string{"work"},
			},
		}},
	})
	if !envelope.OK || !envelope.Truncated || len(envelope.Results) != 1 ||
		envelope.Results[0].Discovery == nil || !envelope.Results[0].Discovery.Truncated {
		t.Fatalf("batch Discovery envelope = %#v", envelope)
	}

	zero, err := runMSQL(authorized, engine,
		"SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL :query LIMIT 8 BYTES 4096",
		executor.Parameters{Named: map[string]any{"query": "astronomy"}}, executor.MutationOptions{})
	if err != nil || zero.Discovery == nil || len(zero.Discovery.Candidates) != 0 ||
		len(zero.Discovery.Predictors) != 1 || zero.Discovery.Predictors[0].Status != discovery.PredictorSucceeded ||
		zero.Truncated {
		t.Fatalf("zero-hit fallback = %#v, %v", zero, err)
	}
	for _, source := range []string{
		"SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL 'recovery' LIMIT 0 BYTES 4096",
		"SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL 'recovery' LIMIT 65 BYTES 4096",
		"SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL 'recovery' LIMIT 8 BYTES 255",
	} {
		if _, err := runMSQL(authorized, engine, source, executor.Parameters{}, executor.MutationOptions{}); !hasCode(err, string(result.CodeValidation)) {
			t.Fatalf("%s error = %v", source, err)
		}
	}

	before := *zero.Discovery
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedDictionary := nativecatalog.NewService(nativecatalog.New(reopened), nativecatalog.ServiceOptions{
		IDs: &testIDs{}, Clock: testClock{value: now},
	})
	reopenedRows := NewService(New(reopened), reopenedDictionary, ServiceOptions{
		IDs: &testIDs{}, Clock: testClock{value: now},
	})
	reopenedOutput, err := runMSQL(authorized, executor.New(reopenedDictionary, reopenedRows),
		"SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL 'astronomy' LIMIT 8 BYTES 4096",
		executor.Parameters{}, executor.MutationOptions{})
	if err != nil || reopenedOutput.Discovery == nil || reopenedOutput.Discovery.Snapshot != before.Snapshot ||
		reopenedOutput.Discovery.CatalogRevision != before.CatalogRevision {
		t.Fatalf("reopened discovery = %#v, %v", reopenedOutput, err)
	}
}
