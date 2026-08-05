package retrievalscore_test

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/retrievalscore"
)

func TestEvaluateComputesMacroQualityAndBaselineCostReduction(t *testing.T) {
	suite := retrievalscore.Suite{Version: retrievalscore.SuiteVersion, SuiteID: "suite", DatasetID: "fixture", Split: "dev",
		Queries: []retrievalscore.Query{{QueryID: "q1", Group: "alpha"}, {QueryID: "q2", Group: "alpha"}, {QueryID: "q3", Group: "beta"}},
		Qrels: []retrievalscore.Qrel{{QueryID: "q1", DocumentID: "d1", Relevance: 1}, {QueryID: "q1", DocumentID: "d2", Relevance: 2},
			{QueryID: "q2", DocumentID: "d3", Relevance: 1}, {QueryID: "q3", DocumentID: "d4", Relevance: 1}}}
	sealSuite(t, &suite)
	baseline := retrievalscore.Run{Version: retrievalscore.RunVersion, RunID: "baseline", Arm: "lexical", SuiteHash: suite.Hash,
		Results: []retrievalscore.Result{{QueryID: "q1", Status: "completed", CandidateDocumentIDs: []string{"d1", "x1", "x2", "x3", "x4", "d2"}, InputTokens: 100, ToolCalls: 2},
			{QueryID: "q2", Status: "completed", CandidateDocumentIDs: []string{"x", "d3"}, InputTokens: 100, ToolCalls: 2},
			{QueryID: "q3", Status: "failed", CandidateDocumentIDs: []string{}, InputTokens: 100, ToolCalls: 2}}}
	optimized := baseline
	optimized.RunID, optimized.Arm = "optimized", "hybrid"
	optimized.Results = append([]retrievalscore.Result(nil), baseline.Results...)
	for index := range optimized.Results {
		optimized.Results[index].InputTokens = 60
		optimized.Results[index].ToolCalls = 1
	}
	sealRun(t, &baseline)
	sealRun(t, &optimized)
	report, err := retrievalscore.Evaluate(suite, []retrievalscore.Run{baseline, optimized}, "baseline", 5)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if len(report.Runs) != 2 {
		t.Fatalf("runs = %d", len(report.Runs))
	}
	score := report.Runs[1]
	if score.Metrics.RecallAtK != 0.5 || score.Metrics.HitRateAtK != 2.0/3.0 || score.Metrics.MRR != 0.5 {
		t.Fatalf("quality metrics = %#v", score.Metrics)
	}
	if score.Metrics.InputTokenReduction == nil || *score.Metrics.InputTokenReduction != 0.4 ||
		score.Metrics.ToolCallReduction == nil || *score.Metrics.ToolCallReduction != 0.5 {
		t.Fatalf("reduction metrics = %#v", score.Metrics)
	}
}

func TestEvaluateRejectsTamperedOrIncompleteRuns(t *testing.T) {
	suite := retrievalscore.Suite{Version: retrievalscore.SuiteVersion, SuiteID: "suite", DatasetID: "fixture", Split: "dev",
		Queries: []retrievalscore.Query{{QueryID: "q1", Group: "all"}}, Qrels: []retrievalscore.Qrel{{QueryID: "q1", DocumentID: "d1", Relevance: 1}}}
	sealSuite(t, &suite)
	bad := retrievalscore.Run{Version: retrievalscore.RunVersion, RunID: "bad", Arm: "lexical", SuiteHash: suite.Hash,
		Results: []retrievalscore.Result{{QueryID: "q1", Status: "completed", CandidateDocumentIDs: []string{"d1", "d1"}}}}
	if err := bad.Seal(); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("Run.Seal() error = %v", err)
	}
	failedWithCandidate := retrievalscore.Run{Version: retrievalscore.RunVersion, RunID: "failed", Arm: "lexical", SuiteHash: suite.Hash,
		Results: []retrievalscore.Result{{QueryID: "q1", Status: "failed", CandidateDocumentIDs: []string{"d1"}}}}
	if err := failedWithCandidate.Seal(); err == nil || !strings.Contains(err.Error(), "failed retrieval") {
		t.Fatalf("Run.Seal() failed-candidate error = %v", err)
	}
}

func sealSuite(t *testing.T, suite *retrievalscore.Suite) {
	t.Helper()
	if err := suite.Seal(); err != nil {
		t.Fatalf("Suite.Seal() error = %v", err)
	}
}

func sealRun(t *testing.T, run *retrievalscore.Run) {
	t.Helper()
	if err := run.Seal(); err != nil {
		t.Fatalf("Run.Seal() error = %v", err)
	}
}
