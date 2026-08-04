package assimilationacceptance

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/instance"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
	"github.com/HW-Yue/Memora/sdk/memora"
)

func TestRunnerCompletesFrozenEPUBChainOnCleanInstance(t *testing.T) {
	ctx := context.Background()
	executor, closeInstance := openAcceptanceInstance(t, ctx)
	defer closeInstance()
	createAcceptanceSchema(t, ctx, executor)

	sourceStore, err := agent.OpenSourceStore(filepath.Join(t.TempDir(), "sources"), agent.SourceStoreConfig{
		MaxObjectBytes: 1 << 20, MaxJobBytes: 1 << 20,
		MaxPhysicalBytes: 2 << 20, MaxSourcesPerJob: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	epub, err := agent.NewEPUBAdapter(sourceStore, agent.DefaultEPUBAdapterConfig())
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := agent.OpenDraftClaimLedger(filepath.Join(t.TempDir(), "claims"))
	if err != nil {
		t.Fatal(err)
	}
	authorProvider := &acceptanceAuthorProvider{}
	profile := acceptanceWriteProfile()
	drafter, err := agent.NewAssimilationDrafter(agent.AssimilationDrafterDependencies{
		MSQL: executor, Provider: authorProvider, Ledger: ledger,
	}, agent.AssimilationDrafterConfig{
		ProviderID: "deterministic-author", Model: "deepseek-v4-flash", WriteProfile: profile,
		BootstrapBudget: agent.DefaultBootstrapBudget(), BootstrapProfile: agent.BootstrapProfileAtlasOnly,
		MaxExtentUTF8Bytes: 64 << 10, MaxOutputTokens: 2048, MaxClaims: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	reviewStore, err := agent.OpenAssimilationReviewStore(filepath.Join(t.TempDir(), "reviews"))
	if err != nil {
		t.Fatal(err)
	}
	reviewProvider := &acceptanceReviewProvider{}
	reviewer, err := agent.NewAssimilationReviewer(agent.AssimilationReviewerDependencies{
		Provider: reviewProvider, Store: reviewStore,
		Challenges: acceptanceChallenges{value: strings.Repeat("4", 64)},
	}, agent.AssimilationReviewerConfig{
		Reviewer: "agent:assimilation-reviewer", ProviderID: "deterministic-reviewer",
		Model: "review-model", MaxOutputTokens: 2048, MaxPeerClaims: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciler, err := agent.NewAssimilationReconciler(executor, profile)
	if err != nil {
		t.Fatal(err)
	}
	clock := &acceptanceClock{now: time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)}
	queryProvider := &acceptanceQueryProvider{}
	queryAgent, err := agent.NewQueryAgent(agent.QueryAgentDependencies{
		MSQL: executor, Provider: queryProvider, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(Dependencies{
		Sources: sourceStore, EPUB: epub, Drafter: drafter, Ledger: ledger,
		Reviewer: reviewer, Reconciler: reconciler, Query: queryAgent,
		Approvals: acceptanceApprovals{}, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := frozenAcceptanceEPUB(t)
	result, err := runner.Run(ctx, Request{
		RunID: "f200-clean-epub-1", JobID: "job-f200", SourceID: "source-f200",
		FileName: "frozen-f200.epub", Payload: payload, SubmissionID: "submission-f200-1",
		ExtentLimits: agent.ReadExtentLimits{MaxNodes: 128, MaxUTF8Bytes: 64 << 10},
		Query: agent.QueryRequest{
			Identity: agent.TraceIdentity{RunID: "f200-clean-epub-1", SessionID: "f200-query-1"},
			Question: "Memora 的项目代号是什么？", Model: "query-model",
			Authorization: protocolmsql.Authorization{
				Version: protocolmsql.AuthorizationVersion, Actor: "acceptance:query",
				AuthorizedDatabases: []string{"work"}, DefaultLevel: protocolmsql.LevelRead,
			},
			BootstrapBudget: agent.DefaultBootstrapBudget(), BootstrapProfile: agent.BootstrapProfileAtlasOnly,
			Budget: agent.DefaultQueryBudget(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Validate() != nil || result.Status != StatusSucceeded || result.Extents != 1 ||
		result.Claims != 1 || result.Reviews != 1 || result.Answer != "Memora 的项目代号是 Aurora。" ||
		result.SelectEvidenceRows != 1 || result.SourceReceipt.Status != protocolmsql.AssimilationCommitted ||
		len(result.SourceReceipt.Statements) != 1 || len(result.SourceReceipt.Statements[0].ObjectIDs) != 1 ||
		result.DraftProviderCalls != 1 || result.ReviewProviderCalls != 1 || result.QueryProviderCalls != 2 ||
		result.Cleanup.ReferencesRemoved != 1 || result.Cleanup.ObjectsRemoved != 1 || len(result.Stages) != 8 {
		t.Fatalf("acceptance result = %#v", result)
	}
	rowID, _ := result.Query.Evidence[0].Result.Rows[0]["row_id"].(string)
	if rowID == "" || rowID != result.SourceReceipt.Statements[0].ObjectIDs[0] {
		t.Fatalf("receipt/evidence RowID = %q / %#v", rowID, result.SourceReceipt.Statements[0].ObjectIDs)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"Aurora is the Memora project codename", "INSERT INTO", "SELECT row_id",
		agent.AssimilationDraftSystemPrompt, agent.AssimilationReviewSystemPrompt, agent.QueryAgentSystemPrompt,
	} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("acceptance report leaked %q: %s", forbidden, encoded)
		}
	}
	stats, err := sourceStore.Stats()
	if err != nil || stats.References != 0 || stats.Objects != 0 || stats.PhysicalBytes != 0 {
		t.Fatalf("SourceStore stats = %#v, %v", stats, err)
	}
}

func acceptanceWriteProfile() agent.WriteProfile {
	return agent.WriteProfile{
		Version: agent.WriteProfileVersion, ProfileID: "f200-assimilation",
		Actor: "agent:assimilation-author", AuthorizedDatabases: []string{"work"},
		MaxStatements: 4, MaxAffectedRows: 1,
	}
}

type acceptanceExecutor struct{ client *memora.Client }

func (executor acceptanceExecutor) ExecuteMSQL(
	ctx context.Context,
	request protocolmsql.Request,
) (protocolmsql.Envelope, error) {
	return executor.client.Execute(ctx, request)
}

func openAcceptanceInstance(
	t *testing.T,
	ctx context.Context,
) (acceptanceExecutor, func()) {
	t.Helper()
	dataDirectory := filepath.Join(t.TempDir(), "instance")
	if _, err := instance.Initialize(ctx, dataDirectory, instance.Options{}); err != nil {
		t.Fatal(err)
	}
	daemonContext, cancel := context.WithCancel(ctx)
	ready := make(chan daemon.State, 1)
	done := make(chan error, 1)
	go func() { done <- daemon.Run(daemonContext, dataDirectory, ready) }()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("daemon start = %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("daemon start timed out")
	}
	client, err := memora.Dial(ctx, memora.DialOptions{DataDir: dataDirectory})
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	return acceptanceExecutor{client: client}, func() {
		_ = client.Close()
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("daemon stop = %v", err)
		}
	}
}

func createAcceptanceSchema(t *testing.T, ctx context.Context, executor acceptanceExecutor) {
	t.Helper()
	authorization := protocolmsql.Authorization{
		Version: protocolmsql.AuthorizationVersion, Actor: "acceptance:setup",
		AuthorizedDatabases: []string{"work"}, DefaultLevel: protocolmsql.LevelStructural,
	}
	for index, source := range []string{
		"CREATE DATABASE work PURPOSE 'F200 clean acceptance' SCOPE 'Local test'",
		"CREATE TABLE work.notes PURPOSE 'Reviewed semantic notes' ROW SEMANTICS 'One independent fact' (title TEXT(200) PURPOSE 'Fact title' ROLE fact, body TEXT(4000) PURPOSE 'Fact body' ROLE fact)",
	} {
		envelope, err := executor.ExecuteMSQL(ctx, protocolmsql.Request{
			Version: protocolmsql.RequestVersion, Source: source,
			Statements: []protocolmsql.StatementInput{{Authorization: authorization}},
		})
		if err != nil || !envelope.OK {
			t.Fatalf("schema statement %d = %#v, %v", index, envelope, err)
		}
	}
}

type acceptanceAuthorProvider struct {
	mu    sync.Mutex
	calls int
}

func (provider *acceptanceAuthorProvider) Complete(
	_ context.Context,
	request agent.ProviderRequest,
) (agent.ProviderResponse, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.calls++
	var input struct {
		Extent agent.ReadExtent `json:"extent"`
	}
	if len(request.Messages) != 2 || json.Unmarshal([]byte(request.Messages[1].Content), &input) != nil {
		return agent.ProviderResponse{}, errors.New("invalid draft request")
	}
	nodeID := ""
	for _, node := range input.Extent.Nodes {
		if strings.Contains(node.Text, "Aurora") {
			nodeID = node.NodeID
			break
		}
	}
	if nodeID == "" {
		return agent.ProviderResponse{}, errors.New("fact node missing")
	}
	arguments, _ := json.Marshal(map[string]any{"claims": []any{map[string]any{
		"claim_id": "claim-aurora", "source_node_ids": []string{nodeID},
		"msql":                    "INSERT INTO work.notes (title, body) VALUES (:title, :body)",
		"named":                   map[string]any{"title": "Memora project codename", "body": "Aurora"},
		"expected_schema_version": 1, "max_affected_rows": 1,
		"reason": "store the reviewed project codename",
	}}})
	return acceptanceToolResponse(request.Model, agent.AssimilationDraftToolName, "draft-f200", arguments), nil
}

type acceptanceReviewProvider struct {
	mu    sync.Mutex
	calls int
}

func (provider *acceptanceReviewProvider) Complete(
	_ context.Context,
	request agent.ProviderRequest,
) (agent.ProviderResponse, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.calls++
	var input struct {
		Challenge       string                `json:"challenge"`
		Batch           agent.DraftClaimBatch `json:"batch"`
		NumericEvidence map[string][]struct {
			ID string `json:"id"`
		} `json:"numeric_evidence"`
	}
	if len(request.Messages) != 2 || json.Unmarshal([]byte(request.Messages[1].Content), &input) != nil {
		return agent.ProviderResponse{}, errors.New("invalid review request")
	}
	checks := make([]any, len(input.Batch.Claims))
	for index, claim := range input.Batch.Claims {
		nodes := make([]string, len(claim.Sources))
		for sourceIndex, source := range claim.Sources {
			nodes[sourceIndex] = source.NodeID
		}
		numeric := make([]string, len(input.NumericEvidence[claim.ClaimID]))
		for evidenceIndex, evidence := range input.NumericEvidence[claim.ClaimID] {
			numeric[evidenceIndex] = evidence.ID
		}
		checks[index] = map[string]any{
			"claim_id": claim.ClaimID, "source_node_ids": nodes, "numeric_evidence_ids": numeric,
			"anchor_verified": true, "numbers_verified": true, "conflict_free": true,
			"not_raw_copy": true, "decision": "accept", "finding_codes": []string{},
		}
	}
	arguments, _ := json.Marshal(map[string]any{"challenge": input.Challenge, "checks": checks})
	return acceptanceToolResponse(request.Model, agent.AssimilationReviewToolName, "review-f200", arguments), nil
}

type acceptanceQueryProvider struct {
	mu    sync.Mutex
	calls int
}

func (provider *acceptanceQueryProvider) Complete(
	_ context.Context,
	request agent.ProviderRequest,
) (agent.ProviderResponse, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.calls++
	if provider.calls == 1 {
		arguments, _ := json.Marshal(map[string]any{
			"source":     "SELECT row_id, title, body FROM work.notes WHERE title = :title LIMIT 1",
			"statements": []any{map[string]any{"named": map[string]any{"title": "Memora project codename"}}},
		})
		return acceptanceToolResponse(request.Model, agent.QueryExecuteMSQLToolName, "query-f200", arguments), nil
	}
	return agent.ProviderResponse{
		Version: agent.ProviderResponseVersion, Model: request.Model,
		Message:      agent.ProviderMessage{Role: agent.ProviderRoleAssistant, Content: "Memora 的项目代号是 Aurora。"},
		FinishReason: agent.ProviderFinishStop,
		Usage:        agent.ProviderUsage{InputTokens: 80, OutputTokens: 12, TotalTokens: 92},
	}, nil
}

func acceptanceToolResponse(model, tool, callID string, arguments []byte) agent.ProviderResponse {
	return agent.ProviderResponse{
		Version: agent.ProviderResponseVersion, Model: model,
		Message: agent.ProviderMessage{Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{{
			ID: callID, Name: tool, Arguments: append(json.RawMessage(nil), arguments...),
		}}},
		FinishReason: agent.ProviderFinishToolCalls,
		Usage:        agent.ProviderUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
	}
}

type acceptanceChallenges struct{ value string }

func (source acceptanceChallenges) NextAssimilationReviewChallenge(context.Context) (string, error) {
	return source.value, nil
}

type acceptanceApprovals struct{}

func (acceptanceApprovals) ApproveAssimilationPlan(
	_ context.Context,
	plan protocolmsql.AssimilationPlan,
) (agent.AssimilationPlanApproval, error) {
	return agent.AssimilationPlanApproval{
		Version: agent.AssimilationPlanApprovalVersion, PlanSHA256: plan.SHA256,
		ApprovedBy: "acceptance:user", Confirmed: true,
	}, nil
}

type acceptanceClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *acceptanceClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	value := clock.now
	clock.now = clock.now.Add(time.Millisecond)
	return value
}

func frozenAcceptanceEPUB(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	write := func(name, body string, method uint16) {
		header := &zip.FileHeader{Name: name, Method: method}
		header.SetModTime(time.Unix(0, 0).UTC())
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, body); err != nil {
			t.Fatal(err)
		}
	}
	write("mimetype", "application/epub+zip", zip.Store)
	write("META-INF/container.xml", `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`, zip.Deflate)
	write("OPS/package.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Memora Frozen Acceptance</dc:title><dc:language>en</dc:language></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="chapter"/></spine></package>`, zip.Deflate)
	write("OPS/nav.xhtml", `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><body><nav epub:type="toc" xmlns:epub="http://www.idpf.org/2007/ops"><ol><li><a href="chapter.xhtml">Project Identity</a></li></ol></nav></body></html>`, zip.Deflate)
	write("OPS/chapter.xhtml", `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><body><h1>Project Identity</h1><p>Aurora is the Memora project codename.</p></body></html>`, zip.Deflate)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func acceptanceSHA256(payload []byte) string {
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}
