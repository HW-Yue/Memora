package storygate_test

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/hostcontract"
	"github.com/HW-Yue/Memora/internal/storygate"
)

func TestAIJourneySuiteIsBlindCompleteAndDeterministic(t *testing.T) {
	t.Parallel()

	first, err := storygate.GenerateAIJourneySuite()
	if err != nil {
		t.Fatal(err)
	}
	second, err := storygate.GenerateAIJourneySuite()
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash != second.Hash || len(first.Stories) != len(storygate.RequiredStories()) || len(first.Hash) != 64 {
		t.Fatalf("suite identity = %#v / %#v", first, second)
	}
	for _, specification := range first.Stories {
		task, err := first.Task(specification.Story)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(task.Prompt)
		for _, forbidden := range []string{"select ", "show ", "insert ", "update ", "delete ", "db_", "tbl_", "route_", "row_"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("story %s prompt leaks %q: %q", specification.Story, forbidden, task.Prompt)
			}
		}
		for _, check := range specification.RequiredChecks {
			if strings.Contains(lower, strings.ToLower(check)) {
				t.Errorf("story %s prompt leaks check %q", specification.Story, check)
			}
		}
		if task.CorpusDigest != first.Hash || len(task.AuthorizedDatabases) != 1 ||
			task.AuthorizedDatabases[0] != "work" || task.Budget.MaxToolCalls != 64 ||
			task.Budget.MaxContextCharacters != 12000 || task.Budget.MaxElapsedMillis != 600000 {
			t.Fatalf("story %s Task = %#v", specification.Story, task)
		}
	}
}

func TestAIStoryGateRequiresEveryStoryOnBothHostsAndIndependentChecks(t *testing.T) {
	t.Parallel()

	contract, digest := storyContract(t)
	suite, _ := storygate.GenerateAIJourneySuite()
	codex, claude := completeReport("codex"), completeReport("claude")
	codex.TaskContractDigest, claude.TaskContractDigest = digest, digest
	evidence := completeAIEvidence(t, contract, suite, codex)
	report, err := storygate.BuildAIReport(contract, digest, suite, codex, claude, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "passed" || len(report.Journeys) != 2*len(storygate.RequiredStories()) || len(report.Hash) != 64 {
		t.Fatalf("AI report = %#v", report)
	}
	if err := report.ValidateAgainst(contract, digest, suite, codex, claude); err != nil {
		t.Fatal(err)
	}

	missing := append([]storygate.AIJourneyEvidence(nil), evidence[:len(evidence)-1]...)
	if _, err := storygate.BuildAIReport(contract, digest, suite, codex, claude, missing); err == nil ||
		!strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing journey error = %v", err)
	}
	failedCheck := append([]storygate.AIJourneyEvidence(nil), evidence...)
	failedCheck[0].Checks = append([]storygate.OutcomeCheck(nil), failedCheck[0].Checks...)
	failedCheck[0].Checks[0].Passed = false
	if _, err := storygate.BuildAIReport(contract, digest, suite, codex, claude, failedCheck); err == nil ||
		!strings.Contains(err.Error(), "check") {
		t.Fatalf("failed outcome check error = %v", err)
	}
}

func TestAIStoryGateRejectsReceiptDriftAndStrictReportTampering(t *testing.T) {
	t.Parallel()

	contract, digest := storyContract(t)
	suite, _ := storygate.GenerateAIJourneySuite()
	codex, claude := completeReport("codex"), completeReport("claude")
	codex.TaskContractDigest, claude.TaskContractDigest = digest, digest
	evidence := completeAIEvidence(t, contract, suite, codex)
	forged := append([]storygate.AIJourneyEvidence(nil), evidence...)
	forged[0].Receipt.TaskDigest = strings.Repeat("f", 64)
	if _, err := storygate.BuildAIReport(contract, digest, suite, codex, claude, forged); err == nil ||
		!strings.Contains(err.Error(), "receipt") {
		t.Fatalf("forged receipt error = %v", err)
	}
	report, err := storygate.BuildAIReport(contract, digest, suite, codex, claude, evidence)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := storygate.EncodeAIReport(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storygate.DecodeAIReport(encoded); err != nil {
		t.Fatal(err)
	}
	unknown := strings.Replace(string(encoded), "{", `{"unknown":true,`, 1)
	if _, err := storygate.DecodeAIReport([]byte(unknown)); err == nil {
		t.Fatal("unknown AI report field was accepted")
	}
	tampered := strings.Replace(string(encoded), `"status": "passed"`, `"status": "incomplete"`, 1)
	if _, err := storygate.DecodeAIReport([]byte(tampered)); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("tampered AI report error = %v", err)
	}
}

func completeAIEvidence(
	t *testing.T,
	contract hostcontract.Contract,
	suite storygate.AIJourneySuite,
	runtimeReport storygate.Report,
) []storygate.AIJourneyEvidence {
	t.Helper()
	evidence := []storygate.AIJourneyEvidence{}
	profiles := []hostcontract.HostProfile{
		{Surface: "codex", Provider: "openai", Protocol: "openai-compatible", Model: "gpt-live", EndpointHost: "openai.example"},
		{Surface: "claude-code", Provider: "anthropic", Protocol: "anthropic-compatible", Model: "claude-live", EndpointHost: "anthropic.example"},
	}
	for _, specification := range suite.Stories {
		task, err := suite.Task(specification.Story)
		if err != nil {
			t.Fatal(err)
		}
		taskDigest, err := contract.TaskDigest(task)
		if err != nil {
			t.Fatal(err)
		}
		for _, profile := range profiles {
			checks := make([]storygate.OutcomeCheck, 0, len(specification.RequiredChecks))
			for _, name := range specification.RequiredChecks {
				checks = append(checks, storygate.OutcomeCheck{Name: name, Passed: true,
					EvidenceDigest: strings.Repeat("e", 64)})
			}
			evidence = append(evidence, storygate.AIJourneyEvidence{
				Story: specification.Story, TaskDigest: taskDigest,
				Receipt: hostcontract.Receipt{
					Version: hostcontract.ReceiptVersion, TaskDigest: taskDigest,
					SkillDigest: runtimeReport.CanonicalDigest, ProtocolDigest: runtimeReport.ProtocolDigest,
					Host: profile, Status: "completed", AnswerDigest: strings.Repeat("d", 64),
					ToolCalls: []hostcontract.ToolCall{{Sequence: 1, Command: "query",
						ResultDigest: strings.Repeat("c", 64), ContextCharacters: 100, ElapsedMillis: 10}},
					Usage: hostcontract.Usage{InputTokens: 20, OutputTokens: 5,
						ContextCharacters: 100, ElapsedMillis: 10},
				}, Checks: checks,
			})
		}
	}
	return evidence
}

func storyContract(t *testing.T) (hostcontract.Contract, string) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	contract, digest, err := hostcontract.Load(filepath.Join(root, "skills", "memora", "host-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	return contract, digest
}
