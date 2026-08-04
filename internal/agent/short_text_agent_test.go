package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestShortTextAgentDraftApproveCommitAndVerify(t *testing.T) {
	t.Parallel()

	request := validShortTextRequest()
	bootstrap := shortTextBootstrapEnvelope()
	insert := shortTextInsertEnvelope()
	verified := shortTextSelectEnvelope()
	msql := &shortTextMSQL{responses: []protocolmsql.Envelope{bootstrap, insert, verified}}
	provider := &shortTextProvider{response: shortTextToolResponse(validShortTextToolArguments(t))}
	subject := newShortTextAgent(t, msql, provider)

	plan, err := subject.Draft(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Validate() != nil || plan.Version != agent.ShortTextPlanVersion || plan.Database != "work" ||
		plan.Table != "notes" || !validTestSHA256(plan.SourceContentSHA256) || !validTestSHA256(plan.SHA256) {
		t.Fatalf("plan = %#v", plan)
	}
	if msql.Calls() != 1 || provider.Calls() != 1 {
		t.Fatalf("Draft calls = MSQL %d Provider %d", msql.Calls(), provider.Calls())
	}
	statement := plan.Proposal.Request.Statements[0]
	if statement.Mutation.Actor != "agent:short-text" || statement.Mutation.Source != request.SourceID ||
		statement.Mutation.SourceKind != protocolmsql.SourceDocumentAnchor ||
		statement.Mutation.SourceLocator != request.SourceLocator ||
		statement.Mutation.SourceContentHash != plan.SourceContentSHA256 ||
		statement.Mutation.ExpectedSchemaVersion != 1 || statement.Mutation.ExpectedRevision != 0 ||
		statement.Mutation.MaxAffectedRows != 1 || len(statement.Mutation.RouteLeafIDs) != 1 {
		t.Fatalf("prepared mutation = %#v", statement.Mutation)
	}
	traceJSON, err := json.Marshal(plan.Trace)
	if err != nil || plan.Trace.Validate() != nil {
		t.Fatalf("Trace = %#v, %v", plan.Trace, err)
	}
	for _, secret := range []string{request.Content, "INSERT INTO work.notes", "complete semantic module"} {
		if strings.Contains(string(traceJSON), secret) {
			t.Fatalf("Draft Trace leaked %q: %s", secret, traceJSON)
		}
	}
	providerRequest := provider.Requests()[0]
	if providerRequest.ToolChoice != agent.ProviderToolChoiceRequired || len(providerRequest.Tools) != 1 ||
		providerRequest.Tools[0].Name != agent.ShortTextProposeToolName ||
		!strings.Contains(providerRequest.Messages[1].Content, request.Content) {
		t.Fatalf("Provider request = %#v", providerRequest)
	}

	if _, err := subject.Commit(context.Background(), plan, agent.WriteApproval{}); !errors.Is(err, agent.ErrWriteApprovalRequired) || msql.Calls() != 1 {
		t.Fatalf("unapproved Commit() error=%v calls=%d", err, msql.Calls())
	}
	receipt, err := subject.Commit(context.Background(), plan, approveWrite(plan.Proposal))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Validate() != nil || receipt.Status != agent.ShortTextStatusVerified ||
		receipt.RowID != "row-short-1" || receipt.Revision != 1 || receipt.CommitSequence != 8 ||
		receipt.Verification == nil || receipt.Verification.Result.Rows[0]["title"] != "Memora" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if msql.Calls() != 3 || provider.Calls() != 1 {
		t.Fatalf("full journey calls = MSQL %d Provider %d", msql.Calls(), provider.Calls())
	}
	calls := msql.Requests()
	if calls[1].Source != plan.Proposal.Request.Source || calls[1].Statements[0].Authorization.Approval == nil {
		t.Fatalf("commit request = %#v", calls[1])
	}
	wantVerifySource := "SELECT * FROM `work`.`notes` WHERE row_id = :row LIMIT 1"
	if calls[2].Source != wantVerifySource || calls[2].Statements[0].Parameters.Named["row"] != "row-short-1" ||
		calls[2].Statements[0].Authorization.DefaultLevel != protocolmsql.LevelRead {
		t.Fatalf("verify request = %#v", calls[2])
	}
}

func TestShortTextAgentFailsClosedOnInvalidSourceAndToolOutput(t *testing.T) {
	t.Parallel()

	t.Run("source budget before calls", func(t *testing.T) {
		msql := &shortTextMSQL{}
		provider := &shortTextProvider{}
		subject := newShortTextAgent(t, msql, provider)
		request := validShortTextRequest()
		request.Content = strings.Repeat("x", 32<<10+1)
		if _, err := subject.Draft(context.Background(), request); !errors.Is(err, agent.ErrInvalidShortTextRequest) {
			t.Fatalf("Draft() error = %v", err)
		}
		if msql.Calls() != 0 || provider.Calls() != 0 {
			t.Fatalf("invalid source calls = %d/%d", msql.Calls(), provider.Calls())
		}
	})

	tests := []struct {
		name     string
		response agent.ProviderResponse
	}{
		{name: "direct answer", response: queryProviderResponse(agent.ProviderFinishStop, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, Content: "skip the tool",
		})},
		{name: "wrong tool", response: shortTextToolResponse(json.RawMessage(`{"proposal_id":"p","database":"work","table":"notes","source":"INSERT","named":{},"expected_schema_version":1,"route_leaf_ids":["leaf"]}`), "other_tool")},
		{name: "unknown field", response: shortTextToolResponse(json.RawMessage(`{"proposal_id":"proposal-bad","database":"work","table":"notes","source":"INSERT INTO work.notes (title) VALUES (:title)","named":{"title":"x"},"expected_schema_version":1,"route_leaf_ids":["leaf"],"reason":"x","extra":true}`))},
		{name: "outside scope", response: shortTextToolResponse(json.RawMessage(`{"proposal_id":"proposal-bad","database":"archive","table":"notes","source":"INSERT INTO archive.notes (title) VALUES (:title)","named":{"title":"x"},"expected_schema_version":1,"route_leaf_ids":["leaf"],"reason":"x"}`))},
		{name: "missing schema guard", response: shortTextToolResponse(json.RawMessage(`{"proposal_id":"proposal-bad","database":"work","table":"notes","source":"INSERT INTO work.notes (title) VALUES (:title)","named":{"title":"x"},"expected_schema_version":0,"route_leaf_ids":["leaf"],"reason":"x"}`))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			msql := &shortTextMSQL{responses: []protocolmsql.Envelope{shortTextBootstrapEnvelope()}}
			provider := &shortTextProvider{response: test.response}
			subject := newShortTextAgent(t, msql, provider)
			if _, err := subject.Draft(context.Background(), validShortTextRequest()); !errors.Is(err, agent.ErrInvalidShortTextToolCall) {
				t.Fatalf("Draft() error = %v", err)
			}
			if msql.Calls() != 1 || provider.Calls() != 1 {
				t.Fatalf("rejected tool calls = %d/%d", msql.Calls(), provider.Calls())
			}
		})
	}
}

func TestShortTextAgentDistinguishesCommitFailureFromCommittedUnverified(t *testing.T) {
	t.Parallel()

	t.Run("commit failed without SELECT", func(t *testing.T) {
		failed := shortTextFailedEnvelope("insert-failed", "INSERT", "constraint")
		msql := &shortTextMSQL{responses: []protocolmsql.Envelope{shortTextBootstrapEnvelope(), failed}}
		provider := &shortTextProvider{response: shortTextToolResponse(validShortTextToolArguments(t))}
		subject := newShortTextAgent(t, msql, provider)
		plan, err := subject.Draft(context.Background(), validShortTextRequest())
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := subject.Commit(context.Background(), plan, approveWrite(plan.Proposal))
		if !errors.Is(err, agent.ErrShortTextCommit) || receipt.Status != agent.ShortTextStatusCommitFailed ||
			receipt.Commit.OK || msql.Calls() != 2 {
			t.Fatalf("Commit() = %#v, %v calls=%d", receipt, err, msql.Calls())
		}
		if receipt.Validate() != nil {
			t.Fatalf("failed receipt Validate() = %v", receipt.Validate())
		}
	})

	t.Run("committed but SELECT failed", func(t *testing.T) {
		failedSelect := shortTextFailedEnvelope("select-failed", "SELECT", "temporary read failure")
		msql := &shortTextMSQL{responses: []protocolmsql.Envelope{
			shortTextBootstrapEnvelope(), shortTextInsertEnvelope(), failedSelect,
		}}
		provider := &shortTextProvider{response: shortTextToolResponse(validShortTextToolArguments(t))}
		subject := newShortTextAgent(t, msql, provider)
		plan, err := subject.Draft(context.Background(), validShortTextRequest())
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := subject.Commit(context.Background(), plan, approveWrite(plan.Proposal))
		if !errors.Is(err, agent.ErrShortTextVerification) ||
			receipt.Status != agent.ShortTextStatusCommittedUnverified || !receipt.Commit.OK ||
			receipt.RowID != "row-short-1" || receipt.Verification != nil || msql.Calls() != 3 {
			t.Fatalf("Commit() = %#v, %v calls=%d", receipt, err, msql.Calls())
		}
		if receipt.Validate() != nil {
			t.Fatalf("unverified receipt Validate() = %v", receipt.Validate())
		}
	})
}

func newShortTextAgent(
	t *testing.T,
	msql agent.MSQLExecutor,
	provider agent.Provider,
) *agent.ShortTextAgent {
	t.Helper()
	bootstrapBudget := agent.DefaultBootstrapBudget()
	bootstrapBudget.RootTableLimit = 0
	subject, err := agent.NewShortTextAgent(agent.ShortTextAgentDependencies{
		MSQL: msql, Provider: provider,
		Clock: &queryClock{next: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)},
	}, agent.ShortTextAgentConfig{
		Model: "model-test", WriteProfile: agent.WriteProfile{
			Version: agent.WriteProfileVersion, ProfileID: "profile-short-text",
			Actor: "agent:short-text", AuthorizedDatabases: []string{"work"},
			MaxStatements: 1, MaxAffectedRows: 1,
		},
		BootstrapBudget: bootstrapBudget, BootstrapProfile: agent.DefaultBootstrapProfile(),
		MaxSourceUTF8Bytes: 32 << 10, MaxOutputTokens: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	return subject
}

func validShortTextRequest() agent.ShortTextRequest {
	return agent.ShortTextRequest{
		Identity: agent.TraceIdentity{RunID: "run-short-text", SessionID: "session-short-text"},
		Intent:   "Store the current Memora design note", Title: "Memora design",
		Content:  "A short source that should become one complete semantic module.",
		SourceID: "source:web:memora", SourceLocator: "https://example.test/memora",
	}
}

func validShortTextToolArguments(t *testing.T) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{
		"proposal_id": "proposal-short-text", "database": "work", "table": "notes",
		"source":                  "INSERT INTO work.notes (title, body) VALUES (:title, :body)",
		"named":                   map[string]any{"title": "Memora", "body": "complete semantic module"},
		"expected_schema_version": 1, "route_leaf_ids": []string{"leaf-design"},
		"reason": "store one complete semantic module",
	})
}

func shortTextToolResponse(arguments json.RawMessage, names ...string) agent.ProviderResponse {
	name := agent.ShortTextProposeToolName
	if len(names) > 0 {
		name = names[0]
	}
	return queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
		Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{{
			ID: "call-short-write", Name: name, Arguments: arguments,
		}},
	})
}

func shortTextBootstrapEnvelope() protocolmsql.Envelope {
	return bootstrapEnvelope(
		atlasResult(0, []protocolmsql.Row{{
			"kind": "table", "database_id": "db-work", "database": "work",
			"table_id": "table-notes", "table": "notes", "purpose": "semantic notes",
		}}, page("atlas-short", false, "")),
		lexicalResult(1, []protocolmsql.Row{}, page("lexical-short", false, "")),
	)
}

func shortTextInsertEnvelope() protocolmsql.Envelope {
	revision, sequence := uint64(1), uint64(8)
	return queryEnvelope("insert-short", protocolmsql.StatementResult{
		Index: 0, Statement: "INSERT", Source: "INSERT INTO work.notes (title, body) VALUES (:title, :body)",
		Status: "succeeded", Columns: []protocolmsql.Column{{Name: "row_id", Type: "TEXT"}},
		Rows: []protocolmsql.Row{{"row_id": "row-short-1"}}, AffectedRows: 1,
		Revision: &revision, CommitSequence: &sequence, Warnings: []protocolmsql.Notice{},
	})
}

func shortTextSelectEnvelope() protocolmsql.Envelope {
	return queryEnvelope("select-short", protocolmsql.StatementResult{
		Index: 0, Statement: "SELECT", Source: "SELECT * FROM `work`.`notes` WHERE row_id = :row LIMIT 1",
		Status: "succeeded", Columns: []protocolmsql.Column{
			{Name: "row_id", Type: "ID"}, {Name: "revision", Type: "INTEGER"},
			{Name: "commit_sequence", Type: "INTEGER"}, {Name: "title", Type: "TEXT"},
		},
		Rows: []protocolmsql.Row{{
			"row_id": "row-short-1", "revision": uint64(1), "commit_sequence": uint64(8), "title": "Memora",
		}},
		Warnings: []protocolmsql.Notice{},
	})
}

func shortTextFailedEnvelope(requestID, statement, message string) protocolmsql.Envelope {
	index := 0
	return protocolmsql.Envelope{
		Version: protocolmsql.EnvelopeVersion, RequestID: requestID, OK: false,
		Results: []protocolmsql.StatementResult{{
			Index: 0, Statement: statement, Source: statement, Status: "failed",
			Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{}, Warnings: []protocolmsql.Notice{},
			Error: &protocolmsql.ResultError{
				Code: "validation_failed", Message: message, StatementIndex: &index,
			},
		}},
		Warnings: []protocolmsql.Notice{},
	}
}

type shortTextMSQL struct {
	mu        sync.Mutex
	responses []protocolmsql.Envelope
	errors    []error
	requests  []protocolmsql.Request
}

func (executor *shortTextMSQL) ExecuteMSQL(
	_ context.Context,
	request protocolmsql.Request,
) (protocolmsql.Envelope, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	index := len(executor.requests)
	executor.requests = append(executor.requests, request)
	if index < len(executor.errors) && executor.errors[index] != nil {
		return protocolmsql.Envelope{}, executor.errors[index]
	}
	if index >= len(executor.responses) {
		return protocolmsql.Envelope{}, errors.New("unexpected MSQL call")
	}
	return executor.responses[index], nil
}

func (executor *shortTextMSQL) Calls() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return len(executor.requests)
}

func (executor *shortTextMSQL) Requests() []protocolmsql.Request {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return append([]protocolmsql.Request(nil), executor.requests...)
}

type shortTextProvider struct {
	mu       sync.Mutex
	response agent.ProviderResponse
	err      error
	requests []agent.ProviderRequest
}

func (provider *shortTextProvider) Complete(
	_ context.Context,
	request agent.ProviderRequest,
) (agent.ProviderResponse, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.requests = append(provider.requests, request)
	return provider.response, provider.err
}

func (provider *shortTextProvider) Calls() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return len(provider.requests)
}

func (provider *shortTextProvider) Requests() []agent.ProviderRequest {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]agent.ProviderRequest(nil), provider.requests...)
}
