package service_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/catalog"
	msqlservice "github.com/HW-Yue/Memora/internal/msql/service"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestShortTextAgentWritesOneRoutedRowThroughRealMSQLAndReadsItBack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
	database, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Projects",
	})
	if err != nil {
		t.Fatal(err)
	}
	table, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Semantic notes", RowSemantics: "One complete note",
		Columns: []catalog.ColumnDefinition{
			{Name: "title", Type: "TEXT(100)", Purpose: "Title"},
			{Name: "body", Type: "TEXT(2000)", Purpose: "Complete semantic module"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := row.New(databaseStore, dictionary, row.Options{})
	tx, err := rows.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root, err := tx.CreateRouterRoot(ctx, database.ID, "Work topics")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := tx.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "memora", Kind: router.KindLeaf, Purpose: "Memora design",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	service := msqlservice.New(ctx, msqlservice.Config{Catalog: dictionary, Rows: rows})
	defer service.Close()
	session, err := service.OpenSession("agent:short-text-integration")
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := integrationShortBootstrap(database, table, leaf.ID)
	executor := &shortTextServiceAdapter{session: session, bootstrap: bootstrap}
	arguments, err := json.Marshal(map[string]any{
		"proposal_id": "proposal-integration-short", "database": "work", "table": "notes",
		"source":                  "INSERT INTO work.notes (title, body) VALUES (:title, :body)",
		"named":                   map[string]any{"title": "Memora", "body": "A complete routed semantic module."},
		"expected_schema_version": table.SchemaVersion, "route_leaf_ids": []string{leaf.ID},
		"reason": "store the approved short source",
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &integrationShortProvider{response: agent.ProviderResponse{
		Version: agent.ProviderResponseVersion, Model: "model-test",
		Message: agent.ProviderMessage{Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{{
			ID: "call-integration-short", Name: agent.ShortTextProposeToolName, Arguments: arguments,
		}}},
		FinishReason: agent.ProviderFinishToolCalls,
		Usage:        agent.ProviderUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	}}
	bootstrapBudget := agent.DefaultBootstrapBudget()
	bootstrapBudget.RootTableLimit = 0
	subject, err := agent.NewShortTextAgent(agent.ShortTextAgentDependencies{
		MSQL: executor, Provider: provider,
		Clock: &integrationShortClock{next: time.Date(2026, 8, 5, 13, 0, 0, 0, time.UTC)},
	}, agent.ShortTextAgentConfig{
		Model: "model-test", WriteProfile: agent.WriteProfile{
			Version: agent.WriteProfileVersion, ProfileID: "profile-integration-short",
			Actor: "agent:short-text", AuthorizedDatabases: []string{"work"},
			MaxStatements: 1, MaxAffectedRows: 1,
		},
		BootstrapBudget: bootstrapBudget, BootstrapProfile: agent.DefaultBootstrapProfile(),
		MaxSourceUTF8Bytes: 32 << 10, MaxOutputTokens: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := subject.Draft(ctx, agent.ShortTextRequest{
		Identity: agent.TraceIdentity{RunID: "run-integration-short", SessionID: "session-integration-short"},
		Intent:   "Store the Memora note", Title: "Memora",
		Content:  "A short source about the Memora design.",
		SourceID: "source:web:integration", SourceLocator: "https://example.test/integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := subject.Commit(ctx, plan, agent.WriteApproval{
		Version: agent.WriteApprovalVersion, ProposalSHA256: plan.Proposal.SHA256,
		ApprovedBy: "user:integration", Confirmed: true,
	})
	if err != nil || receipt.Status != agent.ShortTextStatusVerified {
		t.Fatalf("Commit() = %#v, %v", receipt, err)
	}
	persisted, err := rows.List(ctx, "work", "notes", 10)
	if err != nil || len(persisted) != 1 || persisted[0].ID != receipt.RowID ||
		persisted[0].Values["body"] != "A complete routed semantic module." {
		t.Fatalf("persisted = %#v, %v", persisted, err)
	}
	memberships, err := rows.RouterMemberships(ctx, persisted[0])
	if err != nil || len(memberships) != 1 || memberships[0].LeafID != leaf.ID {
		t.Fatalf("memberships = %#v, %v", memberships, err)
	}
}

func integrationShortBootstrap(
	database catalog.Database,
	table catalog.Table,
	leafID string,
) protocolmsql.Envelope {
	return protocolmsql.Envelope{
		Version: protocolmsql.EnvelopeVersion, RequestID: "bootstrap-integration", OK: true,
		Results: []protocolmsql.StatementResult{
			{
				Index: 0, Statement: "SHOW_CATALOG_ATLAS", Source: "SHOW CATALOG ATLAS", Status: "succeeded",
				Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{{
					"kind": "table", "database_id": database.ID, "database": database.Name,
					"table_id": table.ID, "table": table.Name, "purpose": table.Purpose,
				}},
				Page:     &protocolmsql.ListPage{Version: "memora.list-page/v1", Limit: 64, Snapshot: "atlas-integration"},
				Warnings: []protocolmsql.Notice{},
			},
			{
				Index: 1, Statement: "SHOW_LEXICAL_LOCATIONS", Source: "SHOW LEXICAL LOCATIONS", Status: "succeeded",
				Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{{
					"kind": "route", "database_id": database.ID, "table_id": table.ID, "route_id": leafID,
				}},
				Page:     &protocolmsql.ListPage{Version: "memora.list-page/v1", Limit: 16, Snapshot: "lexical-integration"},
				Warnings: []protocolmsql.Notice{},
			},
		},
		Warnings: []protocolmsql.Notice{},
	}
}

type shortTextServiceAdapter struct {
	mu        sync.Mutex
	session   agent.MSQLExecutor
	bootstrap protocolmsql.Envelope
}

func (adapter *shortTextServiceAdapter) ExecuteMSQL(
	ctx context.Context,
	request protocolmsql.Request,
) (protocolmsql.Envelope, error) {
	if strings.HasPrefix(request.Source, "SHOW CATALOG ATLAS") {
		adapter.mu.Lock()
		defer adapter.mu.Unlock()
		return adapter.bootstrap, nil
	}
	return adapter.session.ExecuteMSQL(ctx, request)
}

type integrationShortProvider struct {
	response agent.ProviderResponse
}

func (provider *integrationShortProvider) Complete(
	context.Context,
	agent.ProviderRequest,
) (agent.ProviderResponse, error) {
	return provider.response, nil
}

type integrationShortClock struct {
	mu   sync.Mutex
	next time.Time
}

func (clock *integrationShortClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	value := clock.next
	clock.next = clock.next.Add(time.Millisecond)
	return value
}
