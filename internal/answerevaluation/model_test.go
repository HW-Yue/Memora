package answerevaluation_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answercorpus"
	"github.com/HW-Yue/Memora/internal/answerevaluation"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestBuildInputJoinsHiddenTruthAndCanonicalSQLContexts(t *testing.T) {
	bundle, err := answercorpus.Generate()
	if err != nil {
		t.Fatal(err)
	}
	public, private := evaluationArtifacts(t, bundle)
	input, err := answerevaluation.BuildInput(bundle, public, private)
	if err != nil {
		t.Fatal(err)
	}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	if input.Version != answerevaluation.InputVersion || input.Hash == "" || len(input.Cases) != 12 ||
		input.PublicScorecardSHA256 != public.Hash || input.PrivateDiagnosticsSHA256 != private.Hash ||
		input.GroundTruthSHA256 != bundle.GroundTruth.Hash {
		t.Fatalf("evaluation input = %#v", input)
	}
	first := input.Cases[0]
	if first.Reference != bundle.GroundTruth.Cases[0].ReferenceAnswer || first.Response != "answer-case-001" ||
		len(first.RetrievedContexts) != 1 || first.RetrievedContexts[0] != `{"decision":"Use a Memora-owned thin Go loop"}` ||
		strings.Contains(first.RetrievedContexts[0], "row_id") || strings.Contains(first.RetrievedContexts[0], "revision") {
		t.Fatalf("first evaluation case = %#v", first)
	}
	last := input.Cases[11]
	if last.RunnerStatus != "failed" || last.Response != "" || len(last.RetrievedContexts) != 0 ||
		last.Reference != bundle.GroundTruth.Cases[11].ReferenceAnswer {
		t.Fatalf("failed evaluation case = %#v", last)
	}

	tampered := input
	tampered.Cases = append([]answerevaluation.Case(nil), input.Cases...)
	tampered.Cases[0].Reference = "tampered"
	if tampered.Validate() == nil {
		t.Fatal("tampered evaluator input validated")
	}
}

func evaluationArtifacts(t *testing.T, bundle answercorpus.Bundle) (answerbenchmark.PublicScorecard, answerbenchmark.PrivateDiagnostics) {
	t.Helper()
	tasks, err := bundle.Manifest.BlindTasks()
	if err != nil {
		t.Fatal(err)
	}
	public := answerbenchmark.PublicScorecard{
		Version: answerbenchmark.PublicScorecardVersion, RunID: "run-evaluation-fixture",
		CorpusID: bundle.Manifest.CorpusID, CorpusRevision: bundle.Manifest.Revision,
		SnapshotID: bundle.Manifest.Fixture.SnapshotID, SnapshotSHA256: bundle.Manifest.Fixture.SnapshotSHA256,
		ProviderID: "scripted", Model: "model-test", ArmID: "hybrid-default",
		PromptID: "query-agent-v1", CodeRevision: "test-revision",
		QualityStatus: answerbenchmark.QualityNotScored, Cases: make([]answerbenchmark.PublicCase, 0, len(tasks)),
	}
	receipt := answerbenchmark.MaterializationReceipt{
		Version: answerbenchmark.MaterializationReceiptVersion, CorpusID: bundle.Manifest.CorpusID,
		CorpusRevision: bundle.Manifest.Revision, ManifestSHA256: bundle.Manifest.Hash,
		FixtureSnapshotID:     bundle.Manifest.Fixture.SnapshotID,
		FixtureSnapshotSHA256: bundle.Manifest.Fixture.SnapshotSHA256,
		MSQLRequests:          1, MSQLStatements: 1,
		Objects: []answerbenchmark.ObjectMapping{{
			Kind: answerbenchmark.ObjectDatabase, FixtureID: "db_fixture", ActualID: "db_actual", Revision: 1,
		}},
	}
	receipt.Hash = testDigest(t, receipt)
	private := answerbenchmark.PrivateDiagnostics{
		Version: answerbenchmark.PrivateDiagnosticsVersion, RunID: public.RunID, Materialization: receipt,
		Cases: make([]answerbenchmark.PrivateCase, 0, len(tasks)),
	}
	for index, task := range tasks {
		recorder, err := agent.NewTraceRecorder(agent.TraceIdentity{
			RunID: public.RunID, SessionID: public.RunID + ":" + task.CaseID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if index == len(tasks)-1 {
			public.Cases = append(public.Cases, answerbenchmark.PublicCase{
				CaseID: task.CaseID, Question: task.Question, Status: "failed", ErrorCode: "query_failed",
				QualityStatus: answerbenchmark.QualityNotScored,
			})
			private.Cases = append(private.Cases, answerbenchmark.PrivateCase{
				Task: task, Error: "private failure", Evidence: []agent.SelectEvidence{}, Trace: recorder.Snapshot(),
			})
			continue
		}
		public.Cases = append(public.Cases, answerbenchmark.PublicCase{
			CaseID: task.CaseID, Question: task.Question, Status: "succeeded", Answer: "answer-" + task.CaseID,
			QualityStatus: answerbenchmark.QualityNotScored, DurationMicros: uint64(index + 1),
			ProviderCalls: 2, MSQLCalls: 2, ToolCalls: 1, InputTokens: 10, OutputTokens: 2, TotalTokens: 12,
		})
		decision := "value-" + task.CaseID
		if index == 0 {
			decision = "Use a Memora-owned thin Go loop"
		}
		evidence := agent.SelectEvidence{RequestID: "request-" + task.CaseID, Result: protocolmsql.StatementResult{
			Index: 0, Statement: "SELECT", Source: "SELECT row_id, revision, decision FROM projects.decisions LIMIT 1",
			Status: "succeeded", Columns: []protocolmsql.Column{
				{Name: "row_id", Type: "ID"}, {Name: "revision", Type: "INTEGER"}, {Name: "decision", Type: "TEXT"},
			}, Rows: []protocolmsql.Row{{"row_id": "row_actual", "revision": uint64(1), "decision": decision}},
			Warnings: []protocolmsql.Notice{},
		}}
		private.Cases = append(private.Cases, answerbenchmark.PrivateCase{
			Task: task, Evidence: []agent.SelectEvidence{evidence}, Trace: recorder.Snapshot(),
		})
	}
	if err := public.Seal(); err != nil {
		t.Fatal(err)
	}
	private.PublicScorecardSHA256 = public.Hash
	if err := private.Seal(); err != nil {
		t.Fatal(err)
	}
	return public, private
}

func testDigest(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}
