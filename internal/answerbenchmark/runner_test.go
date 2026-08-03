package answerbenchmark_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answercorpus"
)

func TestRunnerMaterializesAndRunsAllBlindTasksThroughQueryAgent(t *testing.T) {
	ctx := context.Background()
	executor, closeInstance := cleanDaemonExecutor(t, ctx)
	defer closeInstance()
	bundle, err := answercorpus.Generate()
	if err != nil {
		t.Fatal(err)
	}
	provider := &answerProvider{}
	runner, err := answerbenchmark.NewRunner(answerbenchmark.RunnerDependencies{
		MSQL: executor, Provider: provider, Clock: &benchmarkClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := runner.Run(ctx, bundle.Manifest, answerbenchmark.RunConfig{
		RunID: "run-f183-fake", ProviderID: "scripted", Model: "model-test",
		ArmID: "hybrid-default", PromptID: "query-agent-v1", CodeRevision: "test-revision",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := public.Validate(); err != nil {
		t.Fatalf("public Validate() = %v", err)
	}
	if err := private.Validate(); err != nil {
		t.Fatalf("private Validate() = %v", err)
	}
	if len(public.Cases) != 12 || len(private.Cases) != 12 || len(private.Materialization.Objects) != 46 ||
		private.PublicScorecardSHA256 != public.Hash || provider.callCount() != 24 {
		t.Fatalf("reports=%#v private cases=%d objects=%d Provider calls=%d", public, len(private.Cases), len(private.Materialization.Objects), provider.callCount())
	}
	for index, testCase := range public.Cases {
		if testCase.CaseID != bundle.Manifest.Cases[index].ID || testCase.Status != "succeeded" ||
			testCase.QualityStatus != answerbenchmark.QualityNotScored || testCase.ProviderCalls != 2 ||
			testCase.ToolCalls != 1 || testCase.MSQLCalls == 0 || testCase.TotalTokens == 0 || testCase.Answer == "" {
			t.Fatalf("public case %d = %#v", index, testCase)
		}
		if len(private.Cases[index].Evidence) != 1 || private.Cases[index].Error != "" ||
			private.Cases[index].Task.CaseID != testCase.CaseID || private.Cases[index].Trace.Validate() != nil {
			t.Fatalf("private case %d = %#v", index, private.Cases[index])
		}
	}
	encodedPublic, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"row_projects_runtime", "route_projects_13_runtime", "SELECT * FROM", "Use a Memora-owned thin Go loop"} {
		if strings.Contains(string(encodedPublic), forbidden) {
			t.Fatalf("public scorecard leaked %q", forbidden)
		}
	}
	encodedPrivate, err := json.Marshal(private)
	if err != nil || !strings.Contains(string(encodedPrivate), "SELECT * FROM") {
		t.Fatalf("private diagnostics did not retain evidence: %s, %v", encodedPrivate, err)
	}
}

type answerProvider struct {
	mu    sync.Mutex
	calls int
}

func (provider *answerProvider) Complete(_ context.Context, request agent.ProviderRequest) (agent.ProviderResponse, error) {
	provider.mu.Lock()
	provider.calls++
	call := provider.calls
	provider.mu.Unlock()
	response := agent.ProviderResponse{
		Version: agent.ProviderResponseVersion, Model: request.Model,
		Usage: agent.ProviderUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
	}
	if request.ToolChoice == agent.ProviderToolChoiceNone {
		response.Message = agent.ProviderMessage{Role: agent.ProviderRoleAssistant, Content: "Synthetic unscored answer."}
		response.FinishReason = agent.ProviderFinishStop
		return response, nil
	}
	question := request.Messages[1].Content
	database, table := "projects", "decisions"
	switch {
	case strings.Contains(question, "time zone"), strings.Contains(question, "编辑器"), strings.Contains(question, "code identifier"):
		database, table = "personal", "preferences"
	case strings.Contains(question, "desert-planet"), strings.Contains(question, "哪本书"), strings.Contains(question, "authors"):
		database, table = "reading", "books"
	}
	source := fmt.Sprintf("SELECT * FROM %s.%s LIMIT 4", database, table)
	arguments, _ := json.Marshal(map[string]any{"source": source, "statements": []any{map[string]any{}}})
	response.Message = agent.ProviderMessage{Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{{
		ID: fmt.Sprintf("call-%d", call), Name: agent.QueryExecuteMSQLToolName, Arguments: arguments,
	}}}
	response.FinishReason = agent.ProviderFinishToolCalls
	return response, nil
}

func (provider *answerProvider) callCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls
}

type benchmarkClock struct {
	mu    sync.Mutex
	nanos int64
}

func (clock *benchmarkClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.nanos += int64(time.Millisecond)
	return time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC).Add(time.Duration(clock.nanos))
}
