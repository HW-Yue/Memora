package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestAssimilationDrafterRecordsAnchoredClaimsWithOneProviderCall(t *testing.T) {
	document, extent := assimilationDraftExtent(t)
	root := t.TempDir()
	ledger, err := agent.OpenDraftClaimLedger(root)
	if err != nil {
		t.Fatal(err)
	}
	msql := &shortTextMSQL{responses: []protocolmsql.Envelope{shortTextBootstrapEnvelope()}}
	provider := &shortTextProvider{response: assimilationDraftToolResponse(
		validAssimilationDraftArguments(t, extent),
	)}
	drafter := newAssimilationDrafter(t, msql, provider, ledger)

	receipt, err := drafter.Draft(context.Background(), assimilationDraftRequest(document, extent))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Validate() != nil || receipt.Replayed || receipt.JobID != "job-draft" ||
		receipt.ExtentSequence != extent.Sequence || receipt.ExtentSHA256 != extent.SHA256 ||
		receipt.ProviderID != "openai-compatible" || receipt.Model != "deepseek-v4-flash" ||
		len(receipt.ClaimIDs) != 2 || !validTestSHA256(receipt.PromptSHA256) ||
		!validTestSHA256(receipt.BatchSHA256) {
		t.Fatalf("receipt = %#v", receipt)
	}
	if msql.Calls() != 1 || provider.Calls() != 1 {
		t.Fatalf("calls = MSQL %d Provider %d", msql.Calls(), provider.Calls())
	}
	providerRequest := provider.Requests()[0]
	if providerRequest.ToolChoice != agent.ProviderToolChoiceRequired || len(providerRequest.Tools) != 1 ||
		providerRequest.Tools[0].Name != agent.AssimilationDraftToolName ||
		providerRequest.MaxOutputTokens != 4096 ||
		!strings.Contains(providerRequest.Messages[1].Content, extent.Nodes[0].Text) ||
		!strings.Contains(providerRequest.Messages[1].Content, `"bootstrap"`) {
		t.Fatalf("Provider request = %#v", providerRequest)
	}

	batch, err := ledger.ReadExtent("job-draft", extent.Sequence)
	if err != nil || batch.Validate() != nil || len(batch.Claims) != 2 {
		t.Fatalf("ReadExtent() = %#v, %v", batch, err)
	}
	for index, claim := range batch.Claims {
		wantNode := extent.Nodes[index]
		if claim.Status != agent.DraftClaimReviewRequired || claim.JobID != "job-draft" || claim.Database != "work" ||
			claim.DocumentSHA256 != document.SHA256 || claim.ExtentSHA256 != extent.SHA256 ||
			claim.ProviderID != "openai-compatible" || claim.Model != "deepseek-v4-flash" ||
			claim.PromptVersion != agent.AssimilationDraftPromptVersion ||
			len(claim.Sources) != 1 || claim.Sources[0].NodeID != wantNode.NodeID ||
			!reflect.DeepEqual(claim.Sources[0].Anchor, wantNode.Anchor) || !validTestSHA256(claim.SHA256) {
			t.Fatalf("claim[%d] = %#v", index, claim)
		}
		mutation := claim.Candidate.Mutation
		if mutation.Actor != "agent:assimilation-author" || mutation.Source != document.Source.SourceID ||
			mutation.SourceKind != protocolmsql.SourceDocumentAnchor ||
			mutation.SourceLocator != document.Source.FileName ||
			mutation.SourceContentHash != document.Source.SHA256 || mutation.ExpectedSchemaVersion != 7 ||
			mutation.MaxAffectedRows != 1 {
			t.Fatalf("claim[%d] mutation = %#v", index, mutation)
		}
	}
	listed, err := ledger.Claims("job-draft")
	if err != nil || !reflect.DeepEqual(listed, batch.Claims) {
		t.Fatalf("Claims() = %#v, %v; want %#v", listed, err, batch.Claims)
	}

	encoded := onlyDraftLedgerFile(t, root)
	info, err := os.Stat(onlyDraftLedgerPath(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("ledger mode = %v", info.Mode().Perm())
	}
	for _, forbidden := range []string{
		"A paragraph with a footnote.", agent.AssimilationDraftSystemPrompt,
		"call-assimilation-draft", `"bootstrap"`,
	} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("ledger persisted raw input %q: %s", forbidden, encoded)
		}
	}
}

func TestAssimilationDrafterRejectsExtentConflictBeforeExternalCalls(t *testing.T) {
	document, extent := assimilationDraftExtent(t)
	ledger, err := agent.OpenDraftClaimLedger(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	drafter := newAssimilationDrafter(t,
		&shortTextMSQL{responses: []protocolmsql.Envelope{shortTextBootstrapEnvelope()}},
		&shortTextProvider{response: assimilationDraftToolResponse(validAssimilationDraftArguments(t, extent))},
		ledger,
	)
	if _, err := drafter.Draft(context.Background(), assimilationDraftRequest(document, extent)); err != nil {
		t.Fatal(err)
	}

	changedDraft := validDocumentIR(t)
	changedDraft.Nodes[3].Text = "A materially changed paragraph with another fact."
	changed, err := agent.SealDocumentIR(changedDraft)
	if err != nil {
		t.Fatal(err)
	}
	coverage, err := agent.NewReadCoverage(changed)
	if err != nil {
		t.Fatal(err)
	}
	changedExtent, err := coverage.Next(agent.ReadExtentLimits{MaxNodes: 2, MaxUTF8Bytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	noMSQL := &shortTextMSQL{}
	noProvider := &shortTextProvider{}
	restarted := newAssimilationDrafter(t, noMSQL, noProvider, ledger)
	if _, err := restarted.Draft(context.Background(), assimilationDraftRequest(changed, changedExtent)); !errors.Is(err, agent.ErrDraftClaimConflict) {
		t.Fatalf("conflicting Draft() error = %v", err)
	}
	if noMSQL.Calls() != 0 || noProvider.Calls() != 0 {
		t.Fatalf("conflict calls = MSQL %d Provider %d", noMSQL.Calls(), noProvider.Calls())
	}
}

func TestAssimilationDrafterReopensAndConcurrentlyReplaysWithoutCalls(t *testing.T) {
	document, extent := assimilationDraftExtent(t)
	root := t.TempDir()
	ledger, err := agent.OpenDraftClaimLedger(root)
	if err != nil {
		t.Fatal(err)
	}
	msql := &shortTextMSQL{responses: []protocolmsql.Envelope{shortTextBootstrapEnvelope()}}
	provider := &shortTextProvider{response: assimilationDraftToolResponse(
		validAssimilationDraftArguments(t, extent),
	)}
	drafter := newAssimilationDrafter(t, msql, provider, ledger)
	request := assimilationDraftRequest(document, extent)

	const callers = 12
	receipts := make([]agent.DraftClaimReceipt, callers)
	errorsSeen := make([]error, callers)
	var wait sync.WaitGroup
	for index := range receipts {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			receipts[index], errorsSeen[index] = drafter.Draft(context.Background(), request)
		}(index)
	}
	wait.Wait()
	for index, err := range errorsSeen {
		if err != nil || receipts[index].Validate() != nil {
			t.Fatalf("Draft[%d] = %#v, %v", index, receipts[index], err)
		}
	}
	if msql.Calls() != 1 || provider.Calls() != 1 {
		t.Fatalf("concurrent calls = MSQL %d Provider %d", msql.Calls(), provider.Calls())
	}

	reopened, err := agent.OpenDraftClaimLedger(root)
	if err != nil {
		t.Fatal(err)
	}
	noMSQL := &shortTextMSQL{}
	noProvider := &shortTextProvider{}
	restarted := newAssimilationDrafter(t, noMSQL, noProvider, reopened)
	replayed, err := restarted.Draft(context.Background(), request)
	if err != nil || !replayed.Replayed || replayed.BatchSHA256 != receipts[0].BatchSHA256 {
		t.Fatalf("replayed Draft() = %#v, %v", replayed, err)
	}
	if noMSQL.Calls() != 0 || noProvider.Calls() != 0 {
		t.Fatalf("reopen replay calls = MSQL %d Provider %d", noMSQL.Calls(), noProvider.Calls())
	}
}

func TestAssimilationDrafterRejectsInvalidOutputWithoutLedgerRecord(t *testing.T) {
	document, extent := assimilationDraftExtent(t)
	valid := validAssimilationDraftArgumentMap(extent)
	tests := []struct {
		name     string
		response agent.ProviderResponse
	}{
		{name: "direct answer", response: assimilationDraftProviderResponse(agent.ProviderFinishStop, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, Content: "write it directly",
		})},
		{name: "unknown node", response: assimilationDraftToolResponse(mustJSON(t, map[string]any{
			"claims": []any{draftClaimArgument("claim-unknown", "node:sha256:"+strings.Repeat("f", 64))},
		}))},
		{name: "unknown field", response: assimilationDraftToolResponse(mustJSON(t, map[string]any{
			"claims": []any{valid["claims"].([]any)[0]}, "raw_source": "forbidden",
		}))},
		{name: "missing schema guard", response: assimilationDraftToolResponse(mustJSON(t, map[string]any{
			"claims": []any{func() map[string]any {
				claim := draftClaimArgument("claim-bad-guard", extent.Nodes[0].NodeID)
				claim["expected_schema_version"] = 0
				return claim
			}()},
		}))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger, err := agent.OpenDraftClaimLedger(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			msql := &shortTextMSQL{responses: []protocolmsql.Envelope{shortTextBootstrapEnvelope()}}
			provider := &shortTextProvider{response: test.response}
			drafter := newAssimilationDrafter(t, msql, provider, ledger)
			if _, err := drafter.Draft(context.Background(), assimilationDraftRequest(document, extent)); !errors.Is(err, agent.ErrInvalidAssimilationDraftToolCall) {
				t.Fatalf("Draft() error = %v", err)
			}
			if _, err := ledger.ReadExtent("job-draft", extent.Sequence); !errors.Is(err, agent.ErrDraftClaimNotFound) {
				t.Fatalf("ReadExtent() error = %v", err)
			}
		})
	}
}

func TestDraftClaimLedgerRepairsTornTailAndRejectsCorruption(t *testing.T) {
	document, extent := assimilationDraftExtent(t)
	root := t.TempDir()
	ledger, err := agent.OpenDraftClaimLedger(root)
	if err != nil {
		t.Fatal(err)
	}
	drafter := newAssimilationDrafter(t,
		&shortTextMSQL{responses: []protocolmsql.Envelope{shortTextBootstrapEnvelope()}},
		&shortTextProvider{response: assimilationDraftToolResponse(validAssimilationDraftArguments(t, extent))},
		ledger,
	)
	if _, err := drafter.Draft(context.Background(), assimilationDraftRequest(document, extent)); err != nil {
		t.Fatal(err)
	}
	path := onlyDraftLedgerPath(t, root)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"torn"`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := agent.OpenDraftClaimLedger(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ReadExtent("job-draft", extent.Sequence); err != nil {
		t.Fatalf("torn-tail ReadExtent() error = %v", err)
	}
	afterRepair := onlyDraftLedgerFile(t, root)
	if bytes.Contains(afterRepair, []byte(`{"torn"`)) || afterRepair[len(afterRepair)-1] != '\n' {
		t.Fatalf("tail was not repaired: %s", afterRepair)
	}

	corrupt := bytes.Replace(afterRepair, []byte("openai-compatible"), []byte("openai-compatiblf"), 1)
	if bytes.Equal(corrupt, afterRepair) {
		t.Fatal("corruption target was absent")
	}
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	corruptLedger, err := agent.OpenDraftClaimLedger(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := corruptLedger.ReadExtent("job-draft", extent.Sequence); !errors.Is(err, agent.ErrDraftClaimLedgerCorrupt) {
		t.Fatalf("corrupt ReadExtent() error = %v", err)
	}
}

func newAssimilationDrafter(
	t *testing.T,
	msql agent.MSQLExecutor,
	provider agent.Provider,
	ledger *agent.DraftClaimLedger,
) *agent.AssimilationDrafter {
	t.Helper()
	budget := agent.DefaultBootstrapBudget()
	budget.RootTableLimit = 0
	drafter, err := agent.NewAssimilationDrafter(agent.AssimilationDrafterDependencies{
		MSQL: msql, Provider: provider, Ledger: ledger,
	}, agent.AssimilationDrafterConfig{
		ProviderID: "openai-compatible", Model: "deepseek-v4-flash",
		WriteProfile: agent.WriteProfile{
			Version: agent.WriteProfileVersion, ProfileID: "profile-assimilation",
			Actor: "agent:assimilation-author", AuthorizedDatabases: []string{"work"},
			MaxStatements: 8, MaxAffectedRows: 4,
		},
		BootstrapBudget: budget, BootstrapProfile: agent.DefaultBootstrapProfile(),
		MaxExtentUTF8Bytes: 64 << 10, MaxOutputTokens: 4096, MaxClaims: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	return drafter
}

func assimilationDraftExtent(t *testing.T) (agent.DocumentIR, agent.ReadExtent) {
	t.Helper()
	document := sealedReadCoverageDocument(t)
	coverage, err := agent.NewReadCoverage(document)
	if err != nil {
		t.Fatal(err)
	}
	extent, err := coverage.Next(agent.ReadExtentLimits{MaxNodes: 2, MaxUTF8Bytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	return document, extent
}

func assimilationDraftRequest(document agent.DocumentIR, extent agent.ReadExtent) agent.AssimilationDraftRequest {
	return agent.AssimilationDraftRequest{
		JobID: "job-draft", Database: "work", SourceID: document.Source.SourceID,
		SourceLocator: document.Source.FileName, SourceSHA256: document.Source.SHA256, Extent: extent,
	}
}

func validAssimilationDraftArguments(t *testing.T, extent agent.ReadExtent) json.RawMessage {
	t.Helper()
	return mustJSON(t, validAssimilationDraftArgumentMap(extent))
}

func validAssimilationDraftArgumentMap(extent agent.ReadExtent) map[string]any {
	return map[string]any{"claims": []any{
		draftClaimArgument("claim-heading", extent.Nodes[0].NodeID),
		draftClaimArgument("claim-paragraph", extent.Nodes[1].NodeID),
	}}
}

func draftClaimArgument(claimID, nodeID string) map[string]any {
	return map[string]any{
		"claim_id": claimID, "source_node_ids": []string{nodeID},
		"msql":                    "INSERT INTO work.notes (title, body) VALUES (:title, :body)",
		"named":                   map[string]any{"title": "Chapter summary", "body": "A bounded semantic fact."},
		"expected_schema_version": 7, "max_affected_rows": 1,
		"route_leaf_ids": []string{"leaf-chapter"}, "reason": "record a supported semantic claim",
	}
}

func assimilationDraftToolResponse(arguments json.RawMessage) agent.ProviderResponse {
	return assimilationDraftProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
		Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{{
			ID: "call-assimilation-draft", Name: agent.AssimilationDraftToolName, Arguments: arguments,
		}},
	})
}

func assimilationDraftProviderResponse(
	finish agent.ProviderFinishReason,
	message agent.ProviderMessage,
) agent.ProviderResponse {
	response := queryProviderResponse(finish, message)
	response.Model = "deepseek-v4-flash"
	return response
}

func onlyDraftLedgerPath(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].IsDir() || filepath.Ext(entries[0].Name()) != ".claims" {
		t.Fatalf("ledger entries = %#v", entries)
	}
	return filepath.Join(root, entries[0].Name())
}

func onlyDraftLedgerFile(t *testing.T, root string) []byte {
	t.Helper()
	encoded, err := os.ReadFile(onlyDraftLedgerPath(t, root))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
