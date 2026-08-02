package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/agent/agenttest"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestBootstrapAssemblesAtlasLexicalAndRootPrefetchWithoutModel(t *testing.T) {
	t.Parallel()

	budget := agent.DefaultBootstrapBudget()
	authorization := bootstrapAuthorization()
	query := "shared MSQL service"
	fake := agenttest.NewScriptedMSQL(
		agenttest.MSQLStep{
			Request: initialBootstrapRequest(query, authorization, budget),
			Envelope: bootstrapEnvelope(
				atlasResult(0, []protocolmsql.Row{
					{"kind": "database", "database_id": "db-work", "database": "work", "purpose": "Work"},
					{"kind": "table", "database_id": "db-work", "database": "work", "table_id": "tbl-notes", "table": "notes", "purpose": "Notes"},
				}, page("atlas-snapshot", false, "")),
				lexicalResult(1, []protocolmsql.Row{{
					"kind": "route", "database_id": "db-work", "table_id": "tbl-notes",
					"object_id": "route-service", "revision": int64(3), "frequency": int64(2),
				}}, page("lexical-snapshot", false, "")),
			),
		},
		agenttest.MSQLStep{
			Request: rootRequest(authorization, budget, tableIdentity{
				DatabaseID: "db-work", TableID: "tbl-notes", Database: "work", Table: "notes",
			}),
			Envelope: bootstrapEnvelope(rootResult(0, []protocolmsql.Row{{
				"route_id": "route-root", "revision": int64(3), "kind": "branch", "purpose": "Architecture",
			}}, page("root-snapshot", false, ""))),
		},
	)
	gateway, err := agent.New(agent.Dependencies{MSQL: fake})
	if err != nil {
		t.Fatal(err)
	}
	assembler, err := agent.NewBootstrapAssembler(gateway)
	if err != nil {
		t.Fatal(err)
	}

	frame, err := assembler.Assemble(context.Background(), agent.BootstrapRequest{
		Query: query, Authorization: authorization, Budget: budget,
	})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if frame.Version != agent.BootstrapFrameVersion || frame.Usage != agent.BootstrapUsageNavigation ||
		frame.Catalog.Snapshot != "atlas-snapshot" || !frame.Catalog.Complete || frame.Catalog.Pages != 1 ||
		len(frame.Catalog.Entries) != 2 || frame.Lexical.Snapshot != "lexical-snapshot" ||
		len(frame.Lexical.Locations) != 1 || frame.Truncated {
		t.Fatalf("Frame = %#v", frame)
	}
	root, found := frame.Root("db-work", "tbl-notes")
	if !found || root.Status != agent.RootPrefetchSucceeded || root.Snapshot != "root-snapshot" ||
		len(root.Routes) != 1 || frame.NeedsRootFallback("db-work", "tbl-notes") {
		t.Fatalf("root prefetch = %#v, found=%t", root, found)
	}
	if !frame.NeedsRootFallback("db-work", "tbl-other") {
		t.Fatal("unprefetched Table did not require fallback")
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != frame.UTF8Bytes || len(encoded) > int(budget.FrameUTF8Bytes) {
		t.Fatalf("encoded bytes = %d, receipt = %d, limit = %d", len(encoded), frame.UTF8Bytes, budget.FrameUTF8Bytes)
	}
	if err := fake.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapContinuesAtlasWithOneStableSnapshot(t *testing.T) {
	t.Parallel()

	budget := agent.DefaultBootstrapBudget()
	authorization := bootstrapAuthorization()
	query := "cold table"
	fake := agenttest.NewScriptedMSQL(
		agenttest.MSQLStep{
			Request: initialBootstrapRequest(query, authorization, budget),
			Envelope: bootstrapEnvelope(
				atlasResult(0, []protocolmsql.Row{{"kind": "database", "database_id": "db-a", "database": "alpha"}}, page("atlas-stable", true, "atlas-next")),
				lexicalResult(1, []protocolmsql.Row{}, page("lexical-empty", false, "")),
			),
		},
		agenttest.MSQLStep{
			Request: atlasContinuationRequest("atlas-next", authorization, budget),
			Envelope: bootstrapEnvelope(atlasResult(0, []protocolmsql.Row{
				{"kind": "table", "database_id": "db-a", "database": "alpha", "table_id": "tbl-cold", "table": "cold"},
			}, page("atlas-stable", false, ""))),
		},
	)
	assembler := bootstrapAssembler(t, fake)
	frame, err := assembler.Assemble(context.Background(), agent.BootstrapRequest{
		Query: query, Authorization: authorization, Budget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !frame.Catalog.Complete || frame.Catalog.Pages != 2 || frame.Catalog.NextCursor != "" ||
		len(frame.Catalog.Entries) != 2 || len(frame.Lexical.Locations) != 0 || len(frame.Roots) != 0 ||
		frame.NeedsRootFallback("db-a", "tbl-cold") != true {
		t.Fatalf("Frame = %#v", frame)
	}
	if err := fake.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapMarksAtlasPartialAtPageLimit(t *testing.T) {
	t.Parallel()

	budget := agent.DefaultBootstrapBudget()
	budget.MaxAtlasPages = 1
	authorization := bootstrapAuthorization()
	query := "partial"
	fake := agenttest.NewScriptedMSQL(agenttest.MSQLStep{
		Request: initialBootstrapRequest(query, authorization, budget),
		Envelope: bootstrapEnvelope(
			atlasResult(0, []protocolmsql.Row{{"kind": "database", "database_id": "db-a", "database": "alpha"}}, page("atlas-partial", true, "atlas-next")),
			lexicalResult(1, []protocolmsql.Row{}, page("lexical-empty", false, "")),
		),
	})
	frame, err := bootstrapAssembler(t, fake).Assemble(context.Background(), agent.BootstrapRequest{
		Query: query, Authorization: authorization, Budget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if frame.Catalog.Complete || frame.Catalog.NextCursor != "atlas-next" || !frame.Truncated {
		t.Fatalf("partial Frame = %#v", frame)
	}
	if err := fake.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapRejectsAtlasSnapshotDriftAndMalformedInitialResult(t *testing.T) {
	t.Parallel()

	budget := agent.DefaultBootstrapBudget()
	authorization := bootstrapAuthorization()
	query := "snapshot"
	t.Run("snapshot drift", func(t *testing.T) {
		fake := agenttest.NewScriptedMSQL(
			agenttest.MSQLStep{
				Request: initialBootstrapRequest(query, authorization, budget),
				Envelope: bootstrapEnvelope(
					atlasResult(0, []protocolmsql.Row{{"kind": "database"}}, page("atlas-one", true, "next")),
					lexicalResult(1, []protocolmsql.Row{}, page("lexical", false, "")),
				),
			},
			agenttest.MSQLStep{
				Request:  atlasContinuationRequest("next", authorization, budget),
				Envelope: bootstrapEnvelope(atlasResult(0, []protocolmsql.Row{{"kind": "table"}}, page("atlas-two", false, ""))),
			},
		)
		_, err := bootstrapAssembler(t, fake).Assemble(context.Background(), agent.BootstrapRequest{
			Query: query, Authorization: authorization, Budget: budget,
		})
		if !errors.Is(err, agent.ErrBootstrapSnapshot) {
			t.Fatalf("Assemble() error = %v, want ErrBootstrapSnapshot", err)
		}
	})
	t.Run("missing page", func(t *testing.T) {
		malformedAtlas := atlasResult(0, []protocolmsql.Row{}, page("ignored", false, ""))
		malformedAtlas.Page = nil
		fake := agenttest.NewScriptedMSQL(agenttest.MSQLStep{
			Request: initialBootstrapRequest(query, authorization, budget),
			Envelope: bootstrapEnvelope(
				malformedAtlas,
				lexicalResult(1, []protocolmsql.Row{}, page("lexical", false, "")),
			),
		})
		_, err := bootstrapAssembler(t, fake).Assemble(context.Background(), agent.BootstrapRequest{
			Query: query, Authorization: authorization, Budget: budget,
		})
		if !errors.Is(err, agent.ErrInvalidBootstrapResult) {
			t.Fatalf("Assemble() error = %v, want ErrInvalidBootstrapResult", err)
		}
	})
}

func TestBootstrapRootFailureAndByteSkipRemainFallbacks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		budget     agent.BootstrapBudget
		rootResult protocolmsql.StatementResult
		wantStatus agent.RootPrefetchStatus
	}{
		{
			name:       "MSQL unavailable",
			budget:     agent.DefaultBootstrapBudget(),
			rootResult: failedRootResult(0, protocolmsql.Code("not_found"), "Router root is unavailable"),
			wantStatus: agent.RootPrefetchUnavailable,
		},
		{
			name: "global byte budget",
			budget: func() agent.BootstrapBudget {
				value := agent.DefaultBootstrapBudget()
				value.FrameUTF8Bytes = 4096
				return value
			}(),
			rootResult: rootResult(0, []protocolmsql.Row{{
				"route_id": "large", "purpose": strings.Repeat("x", 5000),
			}}, page("root-large", false, "")),
			wantStatus: agent.RootPrefetchBudgetSkipped,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorization := bootstrapAuthorization()
			query := "fallback"
			table := tableIdentity{DatabaseID: "db", TableID: "table", Database: "work space", Table: "odd`notes"}
			fake := agenttest.NewScriptedMSQL(
				agenttest.MSQLStep{
					Request: initialBootstrapRequest(query, authorization, test.budget),
					Envelope: bootstrapEnvelope(
						atlasResult(0, []protocolmsql.Row{{
							"kind": "table", "database_id": table.DatabaseID, "database": table.Database,
							"table_id": table.TableID, "table": table.Table,
						}}, page("atlas", false, "")),
						lexicalResult(1, []protocolmsql.Row{{
							"kind": "row", "database_id": table.DatabaseID, "table_id": table.TableID,
						}}, page("lexical", false, "")),
					),
				},
				agenttest.MSQLStep{
					Request:  rootRequest(authorization, test.budget, table),
					Envelope: bootstrapEnvelope(test.rootResult),
				},
			)
			frame, err := bootstrapAssembler(t, fake).Assemble(context.Background(), agent.BootstrapRequest{
				Query: query, Authorization: authorization, Budget: test.budget,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(frame.Roots) != 1 || frame.Roots[0].Status != test.wantStatus ||
				!frame.NeedsRootFallback(table.DatabaseID, table.TableID) {
				t.Fatalf("root fallback Frame = %#v", frame)
			}
			encoded, err := json.Marshal(frame)
			if err != nil || len(encoded) > int(test.budget.FrameUTF8Bytes) || len(encoded) != frame.UTF8Bytes {
				t.Fatalf("Frame bytes = %d/%d receipt=%d err=%v", len(encoded), test.budget.FrameUTF8Bytes, frame.UTF8Bytes, err)
			}
		})
	}
}

func TestBootstrapValidatesRequestAndPropagatesCancellationWithoutRetry(t *testing.T) {
	t.Parallel()

	budget := agent.DefaultBootstrapBudget()
	authorization := bootstrapAuthorization()
	fake := agenttest.NewScriptedMSQL()
	assembler := bootstrapAssembler(t, fake)
	_, err := assembler.Assemble(context.Background(), agent.BootstrapRequest{
		Query: " ", Authorization: authorization, Budget: budget,
	})
	if !errors.Is(err, agent.ErrInvalidBootstrapRequest) || len(fake.Calls()) != 0 {
		t.Fatalf("invalid request error = %v, calls = %d", err, len(fake.Calls()))
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = assembler.Assemble(cancelled, agent.BootstrapRequest{
		Query: "cancelled", Authorization: authorization, Budget: budget,
	})
	if !errors.Is(err, context.Canceled) || len(fake.Calls()) != 0 {
		t.Fatalf("cancelled error = %v, calls = %d", err, len(fake.Calls()))
	}
}

func bootstrapAssembler(t *testing.T, executor agent.MSQLExecutor) *agent.BootstrapAssembler {
	t.Helper()
	gateway, err := agent.New(agent.Dependencies{MSQL: executor})
	if err != nil {
		t.Fatal(err)
	}
	assembler, err := agent.NewBootstrapAssembler(gateway)
	if err != nil {
		t.Fatal(err)
	}
	return assembler
}

type tableIdentity struct {
	DatabaseID string
	TableID    string
	Database   string
	Table      string
}

func initialBootstrapRequest(query string, authorization protocolmsql.Authorization, budget agent.BootstrapBudget) protocolmsql.Request {
	return protocolmsql.Request{
		Version: protocolmsql.RequestVersion,
		Source: "SHOW CATALOG ATLAS LIMIT :atlas_limit BYTES :atlas_bytes COMPACT; " +
			"SHOW LEXICAL LOCATIONS FROM ALL TABLES USING :query LIMIT :location_limit BYTES :lexical_bytes",
		Statements: []protocolmsql.StatementInput{
			{
				Parameters: protocolmsql.Parameters{Named: map[string]any{
					"atlas_limit": int64(budget.AtlasEntryLimit), "atlas_bytes": int64(budget.AtlasUTF8Bytes),
				}},
				Authorization: authorization,
			},
			{
				Parameters: protocolmsql.Parameters{Named: map[string]any{
					"query": query, "location_limit": int64(budget.LexicalLocationLimit),
					"lexical_bytes": int64(budget.LexicalUTF8Bytes),
				}},
				Authorization: authorization,
			},
		},
	}
}

func atlasContinuationRequest(cursor string, authorization protocolmsql.Authorization, budget agent.BootstrapBudget) protocolmsql.Request {
	return protocolmsql.Request{
		Version: protocolmsql.RequestVersion,
		Source:  "SHOW CATALOG ATLAS CURSOR :cursor LIMIT :atlas_limit BYTES :atlas_bytes COMPACT",
		Statements: []protocolmsql.StatementInput{{
			Parameters: protocolmsql.Parameters{Named: map[string]any{
				"cursor": cursor, "atlas_limit": int64(budget.AtlasEntryLimit),
				"atlas_bytes": int64(budget.AtlasUTF8Bytes),
			}},
			Authorization: authorization,
		}},
	}
}

func rootRequest(authorization protocolmsql.Authorization, budget agent.BootstrapBudget, tables ...tableIdentity) protocolmsql.Request {
	statements := make([]protocolmsql.StatementInput, len(tables))
	sources := make([]string, len(tables))
	for index, table := range tables {
		sources[index] = "SHOW ROUTES FROM TABLE " + quoteIdentifier(table.Database) + "." +
			quoteIdentifier(table.Table) + " AT ROOT LIMIT :root_limit"
		statements[index] = protocolmsql.StatementInput{
			Parameters:    protocolmsql.Parameters{Named: map[string]any{"root_limit": int64(budget.RootRouteLimit)}},
			Authorization: authorization,
		}
	}
	return protocolmsql.Request{Version: protocolmsql.RequestVersion, Source: strings.Join(sources, "; "), Statements: statements}
}

func quoteIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func bootstrapAuthorization() protocolmsql.Authorization {
	return protocolmsql.Authorization{
		Version: protocolmsql.AuthorizationVersion, Actor: "agent:bootstrap",
		AuthorizedDatabases: []string{"work"}, DefaultLevel: protocolmsql.LevelRead,
	}
}

func bootstrapEnvelope(results ...protocolmsql.StatementResult) protocolmsql.Envelope {
	ok := true
	for _, result := range results {
		if result.Status != "succeeded" {
			ok = false
		}
	}
	return protocolmsql.Envelope{
		Version: protocolmsql.EnvelopeVersion, RequestID: "scripted", OK: ok,
		Results: results, Warnings: []protocolmsql.Notice{},
	}
}

func atlasResult(index int, rows []protocolmsql.Row, pageValue *protocolmsql.ListPage) protocolmsql.StatementResult {
	return successfulResult(index, "SHOW_CATALOG_ATLAS", rows, pageValue)
}

func lexicalResult(index int, rows []protocolmsql.Row, pageValue *protocolmsql.ListPage) protocolmsql.StatementResult {
	return successfulResult(index, "SHOW_LEXICAL_LOCATIONS", rows, pageValue)
}

func rootResult(index int, rows []protocolmsql.Row, pageValue *protocolmsql.ListPage) protocolmsql.StatementResult {
	return successfulResult(index, "SHOW_ROUTES", rows, pageValue)
}

func successfulResult(index int, statement string, rows []protocolmsql.Row, pageValue *protocolmsql.ListPage) protocolmsql.StatementResult {
	return protocolmsql.StatementResult{
		Index: index, Statement: statement, Source: statement, Status: "succeeded",
		Columns: []protocolmsql.Column{}, Rows: rows, Page: pageValue,
		Truncated: pageValue != nil && pageValue.Truncated,
		NextCursor: func() string {
			if pageValue == nil {
				return ""
			}
			return pageValue.NextCursor
		}(),
		Warnings: []protocolmsql.Notice{},
	}
}

func failedRootResult(index int, code protocolmsql.Code, message string) protocolmsql.StatementResult {
	statementIndex := index
	return protocolmsql.StatementResult{
		Index: index, Statement: "SHOW_ROUTES", Source: "SHOW_ROUTES", Status: "failed",
		Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{}, Warnings: []protocolmsql.Notice{},
		Error: &protocolmsql.ResultError{Code: code, Message: message, StatementIndex: &statementIndex},
	}
}

func page(snapshot string, truncated bool, next string) *protocolmsql.ListPage {
	return &protocolmsql.ListPage{
		Version: "memora.list-page/v1", Limit: 64, Snapshot: snapshot,
		Truncated: truncated, NextCursor: next,
	}
}
