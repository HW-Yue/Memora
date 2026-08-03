package answerrelease_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answerevaluation"
	"github.com/HW-Yue/Memora/internal/answerrelease"
)

func TestBuildPassesCompleteIdentityLockedMatrixAndSelectsLowestContextArm(t *testing.T) {
	evidence := []answerrelease.Evidence{
		releaseEvidence(t, answerbenchmark.ArmAtlasLexicalPrefetch, 30, 0.96, false),
		releaseEvidence(t, answerbenchmark.ArmAtlasOnly, 10, 0.95, false),
		releaseEvidence(t, answerbenchmark.ArmAtlasLexical, 20, 0.97, false),
	}
	report, err := answerrelease.Build(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != answerrelease.StatusPassed || report.DefaultArm != answerbenchmark.ArmAtlasOnly ||
		report.Policy != answerrelease.FixedPolicy() || len(report.Arms) != 3 || report.Hash == "" {
		t.Fatalf("release report = %#v", report)
	}
	wantOrder := []string{
		answerbenchmark.ArmAtlasOnly, answerbenchmark.ArmAtlasLexical, answerbenchmark.ArmAtlasLexicalPrefetch,
	}
	for index, arm := range report.Arms {
		if arm.ArmID != wantOrder[index] || !arm.Eligible || len(arm.Reasons) != 0 ||
			arm.EvidenceStatus != "complete" || arm.Minimums.FactualCorrectness == nil {
			t.Fatalf("arm %d = %#v", index, arm)
		}
	}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := report.ValidateAgainst(evidence); err != nil {
		t.Fatal(err)
	}
	reordered, err := answerrelease.Build([]answerrelease.Evidence{evidence[1], evidence[2], evidence[0]})
	if err != nil || reordered.Hash != report.Hash {
		t.Fatalf("reordered report = %#v, %v", reordered, err)
	}
	wantMean := *report.Arms[0].Metrics.FactualCorrectness.Mean
	*evidence[1].Evaluation.Metrics.FactualCorrectness.Mean = 0
	if *report.Arms[0].Metrics.FactualCorrectness.Mean != wantMean || report.Validate() != nil {
		t.Fatal("release report retained aliases to source evidence")
	}
}

func TestBuildProducesIncompleteAndFailedWithoutDefault(t *testing.T) {
	t.Run("incomplete coverage", func(t *testing.T) {
		evidence := completeReleaseMatrix(t, 0.95)
		evidence[1] = releaseEvidence(t, answerbenchmark.ArmAtlasLexical, 20, 0.95, true)
		report, err := answerrelease.Build(evidence)
		if err != nil {
			t.Fatal(err)
		}
		if report.Status != answerrelease.StatusIncomplete || report.DefaultArm != "" ||
			!contains(report.Arms[1].Reasons, "runner_coverage_incomplete") {
			t.Fatalf("incomplete report = %#v", report)
		}
	})

	t.Run("complete low quality", func(t *testing.T) {
		report, err := answerrelease.Build(completeReleaseMatrix(t, 0.40))
		if err != nil {
			t.Fatal(err)
		}
		if report.Status != answerrelease.StatusFailed || report.DefaultArm != "" {
			t.Fatalf("failed report = %#v", report)
		}
		for _, arm := range report.Arms {
			if arm.Eligible || !contains(arm.Reasons, "factual_correctness_mean_below_threshold") ||
				!contains(arm.Reasons, "faithfulness_mean_below_threshold") {
				t.Fatalf("low-quality arm = %#v", arm)
			}
		}
	})
}

func TestBuildRejectsIdentityBindingAndArmMatrixDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]answerrelease.Evidence) []answerrelease.Evidence
	}{
		{name: "missing arm", mutate: func(values []answerrelease.Evidence) []answerrelease.Evidence { return values[:2] }},
		{name: "duplicate arm", mutate: func(values []answerrelease.Evidence) []answerrelease.Evidence {
			values[2] = values[1]
			return values
		}},
		{name: "unknown arm", mutate: func(values []answerrelease.Evidence) []answerrelease.Evidence {
			values[0].Scorecard.ArmID = "unknown-v1"
			values[0].Scorecard.Hash = ""
			_ = values[0].Scorecard.Seal()
			values[0].Evaluation.PublicScorecardSHA256 = values[0].Scorecard.Hash
			values[0].Evaluation.Hash = ""
			_ = values[0].Evaluation.Seal()
			return values
		}},
		{name: "scorecard binding", mutate: func(values []answerrelease.Evidence) []answerrelease.Evidence {
			values[0].Evaluation.PublicScorecardSHA256 = digest("wrong-scorecard")
			values[0].Evaluation.Hash = ""
			_ = values[0].Evaluation.Seal()
			return values
		}},
		{name: "model identity", mutate: func(values []answerrelease.Evidence) []answerrelease.Evidence {
			values[0].Scorecard.Model = "drifted-model"
			values[0].Scorecard.Hash = ""
			_ = values[0].Scorecard.Seal()
			values[0].Evaluation.PublicScorecardSHA256 = values[0].Scorecard.Hash
			values[0].Evaluation.Hash = ""
			_ = values[0].Evaluation.Seal()
			return values
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := answerrelease.Build(test.mutate(completeReleaseMatrix(t, 0.95)))
			if err == nil {
				t.Fatal("invalid release matrix built a report")
			}
		})
	}
}

func TestBuildRejectsPerCasePerformanceBindingDrift(t *testing.T) {
	evidence := completeReleaseMatrix(t, 0.95)
	evidence[0].Scorecard.Cases[0].InputTokens++
	evidence[0].Scorecard.Cases[0].TotalTokens++
	evidence[0].Scorecard.Hash = ""
	if err := evidence[0].Scorecard.Seal(); err != nil {
		t.Fatal(err)
	}
	evidence[0].Evaluation.PublicScorecardSHA256 = evidence[0].Scorecard.Hash
	evidence[0].Evaluation.Hash = ""
	if err := evidence[0].Evaluation.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := answerrelease.Build(evidence); err == nil {
		t.Fatal("per-case scorecard/evaluation performance drift built a release report")
	}
}

func TestBuildAppliesQualityParityAndPerCaseMinimums(t *testing.T) {
	t.Run("quality parity", func(t *testing.T) {
		evidence := []answerrelease.Evidence{
			releaseEvidence(t, answerbenchmark.ArmAtlasOnly, 10, 0.91, false),
			releaseEvidence(t, answerbenchmark.ArmAtlasLexical, 20, 0.95, false),
			releaseEvidence(t, answerbenchmark.ArmAtlasLexicalPrefetch, 30, 0.95, false),
		}
		report, err := answerrelease.Build(evidence)
		if err != nil {
			t.Fatal(err)
		}
		if report.Status != answerrelease.StatusPassed || report.DefaultArm != answerbenchmark.ArmAtlasLexical ||
			report.Arms[0].Eligible || !contains(report.Arms[0].Reasons, "factual_correctness_delta_above_threshold") ||
			!contains(report.Arms[0].Reasons, "faithfulness_delta_above_threshold") {
			t.Fatalf("quality parity report = %#v", report)
		}
	})

	t.Run("per-case minimum", func(t *testing.T) {
		evidence := completeReleaseMatrix(t, 0.95)
		low := 0.40
		evidence[0].Evaluation.Cases[0].Scores.FactualCorrectness = &low
		mean := (low + 11*0.95) / 12
		evidence[0].Evaluation.Metrics.FactualCorrectness.Mean = &mean
		evidence[0].Evaluation.Hash = ""
		if err := evidence[0].Evaluation.Seal(); err != nil {
			t.Fatal(err)
		}
		report, err := answerrelease.Build(evidence)
		if err != nil {
			t.Fatal(err)
		}
		if report.Arms[0].Eligible || !contains(report.Arms[0].Reasons, "factual_correctness_minimum_below_threshold") ||
			contains(report.Arms[0].Reasons, "factual_correctness_mean_below_threshold") {
			t.Fatalf("per-case minimum report = %#v", report.Arms[0])
		}
	})
}

func TestReleaseReportRejectsTampering(t *testing.T) {
	evidence := completeReleaseMatrix(t, 0.95)
	report, err := answerrelease.Build(evidence)
	if err != nil {
		t.Fatal(err)
	}
	tampered := report
	tampered.DefaultArm = answerbenchmark.ArmAtlasLexicalPrefetch
	tampered.Hash = ""
	if tampered.Seal() == nil || tampered.Validate() == nil || tampered.ValidateAgainst(evidence) == nil {
		t.Fatal("tampered release decision validated")
	}
}

func completeReleaseMatrix(t *testing.T, score float64) []answerrelease.Evidence {
	t.Helper()
	return []answerrelease.Evidence{
		releaseEvidence(t, answerbenchmark.ArmAtlasOnly, 10, score, false),
		releaseEvidence(t, answerbenchmark.ArmAtlasLexical, 20, score, false),
		releaseEvidence(t, answerbenchmark.ArmAtlasLexicalPrefetch, 30, score, false),
	}
}

func releaseEvidence(
	t *testing.T,
	armID string,
	inputTokens uint64,
	score float64,
	oneRunnerFailure bool,
) answerrelease.Evidence {
	t.Helper()
	runID := "run-" + armID
	scorecard := answerbenchmark.PublicScorecard{
		Version: answerbenchmark.PublicScorecardVersion, RunID: runID,
		CorpusID: "answer-retrieval-v1", CorpusRevision: 1, SnapshotID: "fixture-v1",
		SnapshotSHA256: digest("snapshot"), ProviderID: "scripted", Model: "model-v1", ArmID: armID,
		PromptID: "query-agent-v1", CodeRevision: "revision-v1",
		QualityStatus: answerbenchmark.QualityNotScored, Cases: []answerbenchmark.PublicCase{},
	}
	evaluationCases := []answerevaluation.ReportCase{}
	runnerSucceeded, runnerFailed := uint64(0), uint64(0)
	for index := 0; index < 12; index++ {
		caseID := "case-" + string(rune('a'+index))
		failed := oneRunnerFailure && index == 11
		if failed {
			scorecard.Cases = append(scorecard.Cases, answerbenchmark.PublicCase{
				CaseID: caseID, Question: "question " + caseID, Status: "failed", ErrorCode: "query_failed",
				QualityStatus: answerbenchmark.QualityNotScored, DurationMicros: 100, ProviderCalls: 1, MSQLCalls: 1,
			})
			evaluationCases = append(evaluationCases, answerevaluation.ReportCase{
				CaseID: caseID, Status: "runner_failed", DurationMicros: 100, ProviderCalls: 1, MSQLCalls: 1,
				Scores: answerevaluation.MetricScores{},
			})
			runnerFailed++
			continue
		}
		scorecard.Cases = append(scorecard.Cases, answerbenchmark.PublicCase{
			CaseID: caseID, Question: "question " + caseID, Status: "succeeded", Answer: "answer " + caseID,
			QualityStatus: answerbenchmark.QualityNotScored, DurationMicros: 100,
			ProviderCalls: 2, MSQLCalls: 2, ToolCalls: 1,
			InputTokens: inputTokens, OutputTokens: 2, TotalTokens: inputTokens + 2,
		})
		evaluationCases = append(evaluationCases, answerevaluation.ReportCase{
			CaseID: caseID, Status: "scored", DurationMicros: 100,
			ProviderCalls: 2, MSQLCalls: 2, ToolCalls: 1,
			InputTokens: inputTokens, OutputTokens: 2, TotalTokens: inputTokens + 2,
			Scores: releaseScores(score),
		})
		runnerSucceeded++
	}
	if err := scorecard.Seal(); err != nil {
		t.Fatal(err)
	}
	report := answerevaluation.Report{
		Version: answerevaluation.ReportVersion, RunID: runID, CorpusID: scorecard.CorpusID,
		CorpusRevision: scorecard.CorpusRevision, SnapshotSHA256: scorecard.SnapshotSHA256,
		PublicScorecardSHA256: scorecard.Hash, PrivateDiagnosticsSHA256: digest(runID + "-private"),
		GroundTruthSHA256: digest("truth"), EvaluatorInputSHA256: digest(runID + "-input"),
		EvaluatorOutputSHA256: digest(runID + "-output"), AdapterID: "ragas-collections",
		AdapterVersion: "0.4.3", JudgeModel: "judge-v1",
		Counts: answerevaluation.ReportCounts{
			Cases: 12, RunnerSucceeded: runnerSucceeded, RunnerFailed: runnerFailed, Scored: runnerSucceeded,
		},
		Performance: answerevaluation.ReportPerformance{
			LatencyP50Micros: 100, LatencyP95Micros: 100,
			ProviderCalls: runnerSucceeded*2 + runnerFailed, MSQLCalls: runnerSucceeded*2 + runnerFailed,
			ToolCalls: runnerSucceeded, InputTokens: runnerSucceeded * inputTokens,
			OutputTokens: runnerSucceeded * 2, TotalTokens: runnerSucceeded * (inputTokens + 2),
		},
		Metrics: answerevaluation.ReportMetrics{
			FactualCorrectness: releaseAggregate(runnerSucceeded, score),
			Faithfulness:       releaseAggregate(runnerSucceeded, score),
			ContextPrecision:   releaseAggregate(runnerSucceeded, score),
			ContextRecall:      releaseAggregate(runnerSucceeded, score),
		},
		Cases: evaluationCases,
	}
	if err := report.Seal(); err != nil {
		t.Fatal(err)
	}
	return answerrelease.Evidence{Scorecard: scorecard, Evaluation: report}
}

func releaseScores(value float64) answerevaluation.MetricScores {
	return answerevaluation.MetricScores{
		FactualCorrectness: pointer(value), Faithfulness: pointer(value),
		ContextPrecision: pointer(value), ContextRecall: pointer(value),
	}
}

func releaseAggregate(samples uint64, value float64) answerevaluation.MetricAggregate {
	if samples == 0 {
		return answerevaluation.MetricAggregate{}
	}
	return answerevaluation.MetricAggregate{Samples: samples, Mean: pointer(value)}
}

func pointer(value float64) *float64 { return &value }

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func digest(seed string) string {
	value := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(value[:])
}

func TestReleaseReportDoesNotExposeAnswerContent(t *testing.T) {
	report, err := answerrelease.Build(completeReleaseMatrix(t, 0.95))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "answer case") || strings.Contains(string(encoded), "question case") {
		t.Fatal("release report exposed answer content")
	}
}
