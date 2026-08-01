package hostcontract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/hostcontract"
)

func TestCanonicalRealHostContractFreezesTaskBudgetsAndProviderBoundary(t *testing.T) {
	t.Parallel()

	contract, digest, err := hostcontract.Load(filepath.Join(repositoryRoot(t), "skills", "memora", "host-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.Validate(); err != nil {
		t.Fatal(err)
	}
	if contract.Version != hostcontract.ContractVersion || len(digest) != 64 ||
		contract.TaskVersion != hostcontract.TaskVersion || contract.ReceiptVersion != hostcontract.ReceiptVersion {
		t.Fatalf("host contract identity = %#v, %q", contract, digest)
	}
	if contract.Budgets.MaxPromptCharacters != 4096 || contract.Budgets.MaxAuthorizedDatabases != 32 ||
		contract.Budgets.MaxToolCalls != 64 || contract.Budgets.MaxContextCharacters != 12000 ||
		contract.Budgets.MaxElapsedMillis != 600000 {
		t.Fatalf("host contract budgets = %#v", contract.Budgets)
	}
	if contract.Credentials != "never-passed-to-memora" || contract.HiddenReasoning != "forbidden" {
		t.Fatalf("host privacy boundary = %#v", contract)
	}
}

func TestTaskDigestIsHostIndependentAndMatrixRequiresCodexClaudeAndKimi(t *testing.T) {
	t.Parallel()

	contract, _, err := hostcontract.Load(filepath.Join(repositoryRoot(t), "skills", "memora", "host-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	task := hostcontract.Task{
		Version: hostcontract.TaskVersion, ID: "route-ambiguous-001",
		Prompt:              "Find the stored decision about Route fallback.",
		AuthorizedDatabases: []string{"work"}, CorpusDigest: strings.Repeat("a", 64),
		Budget: hostcontract.Budget{MaxToolCalls: 12, MaxContextCharacters: 8000, MaxElapsedMillis: 120000},
	}
	taskDigest, err := contract.TaskDigest(task)
	if err != nil {
		t.Fatal(err)
	}
	shared := func(surface, provider, protocol, model string) hostcontract.Invocation {
		return hostcontract.Invocation{
			Version: hostcontract.InvocationVersion, TaskDigest: taskDigest,
			SkillDigest: strings.Repeat("b", 64), ProtocolDigest: strings.Repeat("c", 64),
			Host: hostcontract.HostProfile{Surface: surface, Provider: provider,
				Protocol: protocol, Model: model, EndpointHost: "provider.example"},
		}
	}
	matrix := []hostcontract.Invocation{
		shared("codex", "openai", "openai-compatible", "gpt-test"),
		shared("claude-code", "anthropic", "anthropic-compatible", "claude-test"),
		shared("codex", "kimi", "openai-compatible", "kimi-test"),
	}
	if err := contract.ValidateMatrix(matrix); err != nil {
		t.Fatal(err)
	}
	if matrix[0].TaskDigest != matrix[1].TaskDigest || matrix[1].TaskDigest != matrix[2].TaskDigest {
		t.Fatal("host identity leaked into Task digest")
	}
	withoutKimi := append([]hostcontract.Invocation(nil), matrix[:2]...)
	if err := contract.ValidateMatrix(withoutKimi); err == nil || !strings.Contains(strings.ToLower(err.Error()), "kimi") {
		t.Fatalf("matrix without Kimi error = %v", err)
	}
	matrix[1].TaskDigest = strings.Repeat("d", 64)
	if err := contract.ValidateMatrix(matrix); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("mixed Task matrix error = %v", err)
	}
}

func TestContractRejectsUnknownFieldsSecretsAndReceiptBudgetEscape(t *testing.T) {
	t.Parallel()

	path := filepath.Join(repositoryRoot(t), "skills", "memora", "host-contract.json")
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(t.TempDir(), "host-contract.json")
	bad := strings.Replace(string(encoded), "{", `{"unknown":true,`, 1)
	if err := os.WriteFile(unknown, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := hostcontract.Load(unknown); err == nil {
		t.Fatal("unknown host contract field was accepted")
	}
	contract, _, err := hostcontract.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	task := hostcontract.Task{
		Version: hostcontract.TaskVersion, ID: "task-1", Prompt: "Query one fact.",
		AuthorizedDatabases: []string{"work"}, CorpusDigest: strings.Repeat("a", 64),
		Budget: hostcontract.Budget{MaxToolCalls: 1, MaxContextCharacters: 100, MaxElapsedMillis: 1000},
	}
	digest, err := contract.TaskDigest(task)
	if err != nil {
		t.Fatal(err)
	}
	receipt := hostcontract.Receipt{
		Version: hostcontract.ReceiptVersion, TaskDigest: digest,
		SkillDigest: strings.Repeat("b", 64), ProtocolDigest: strings.Repeat("c", 64),
		Host: hostcontract.HostProfile{Surface: "codex", Provider: "kimi", Protocol: "openai-compatible",
			Model: "kimi-test", EndpointHost: "provider.example"},
		Status: "completed", AnswerDigest: strings.Repeat("d", 64),
		ToolCalls: []hostcontract.ToolCall{{Sequence: 1, Command: "query", ResultDigest: strings.Repeat("e", 64),
			ContextCharacters: 100, ElapsedMillis: 10}},
		Usage: hostcontract.Usage{InputTokens: 10, OutputTokens: 5, ContextCharacters: 100, ElapsedMillis: 10},
	}
	if err := contract.ValidateReceipt(task, receipt); err != nil {
		t.Fatal(err)
	}
	encodedReceipt, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	withReasoning := strings.Replace(string(encodedReceipt), "{", `{"reasoning":"private",`, 1)
	if _, err := contract.DecodeReceipt(task, []byte(withReasoning)); err == nil {
		t.Fatal("unknown hidden reasoning receipt field was accepted")
	}
	receipt.Host.EndpointHost = "https://user:secret@provider.example/v1"
	if err := contract.ValidateReceipt(task, receipt); err == nil {
		t.Fatal("credential-bearing endpoint was accepted")
	}
	receipt.Host.EndpointHost = "provider.example"
	receipt.ToolCalls = append(receipt.ToolCalls, hostcontract.ToolCall{
		Sequence: 2, Command: "query", ResultDigest: strings.Repeat("f", 64),
	})
	if err := contract.ValidateReceipt(task, receipt); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("over-budget receipt error = %v", err)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
