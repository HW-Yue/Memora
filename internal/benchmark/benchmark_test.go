package benchmark_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/HW-Yue/Memora/internal/benchmark"
)

func TestLoadCanonicalSuiteCoversFiveAINativeJourneysAndFiftyTurns(t *testing.T) {
	t.Parallel()

	suite, err := benchmark.Load(filepath.Join(repositoryRoot(t), "benchmarks", "ai-native-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := map[benchmark.Kind]bool{
		benchmark.KindMultiProject: false, benchmark.KindRevision: false,
		benchmark.KindAssimilation: false, benchmark.KindColdStart: false, benchmark.KindHostSwitch: false,
	}
	for _, scenario := range suite.Scenarios {
		wantKinds[scenario.Kind] = true
		if scenario.Kind == benchmark.KindMultiProject && len(scenario.Turns) != 50 {
			t.Errorf("multi-project turns = %d, want 50", len(scenario.Turns))
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Errorf("canonical suite omits %q", kind)
		}
	}
}

func TestRunnerScoresAllEightQualityDimensionsDeterministically(t *testing.T) {
	t.Parallel()

	suite := benchmark.Suite{
		Version: benchmark.SuiteVersion, ID: "small", Scenarios: []benchmark.Scenario{{
			ID: "scenario", Kind: benchmark.KindRevision, Hosts: []string{"codex", "claude-code"},
			Turns: []benchmark.Turn{{ID: "turn-1", Project: "memora", Intent: "revise", Expected: "revision 2"}},
		}},
	}
	counts := benchmark.Counts{
		WritesCorrect: 9, WritesAttempted: 10, WritesExpected: 12,
		SourceUnitsRead: 8, SourceUnitsRequired: 10, ClaimsCorrect: 7, ClaimsChecked: 8, AnchorsTraceable: 6, AnchorsChecked: 7,
		SchemaDuplicates: 1, SchemaObjects: 20, RetrievalHitsAt5: 9, RetrievalQueries: 10, ReciprocalRankMilli: 8000, NDCGMilli: 8500,
		ContextCharacters: 4000, ToolCalls: 20, AnsweredTurns: 10, IrrelevantRows: 2, SelectedRows: 20,
		UnintendedRows: 0, MutatedRows: 9, RevisionConflictsCaptured: 3, RevisionConflicts: 3,
		TakeoversSucceeded: 2, Takeovers: 2, RecoveriesSucceeded: 4, Recoveries: 4,
		IndexChecksPassed: 5, IndexChecks: 5, ExportHashesMatched: 2, ExportChecks: 2,
	}
	adapter := benchmark.NewScriptedAdapter("memora", map[string]benchmark.Outcome{
		"scenario": {HostResultsEquivalent: true, Counts: counts},
	})
	report, err := benchmark.NewRunner(adapter).Run(context.Background(), suite)
	if err != nil {
		t.Fatal(err)
	}
	metrics := report.Aggregate
	if metrics.WritePrecision != 0.9 || metrics.WriteRecall != 0.75 || metrics.AssimilationCoverage != 0.8 ||
		metrics.RecallAt5 != 0.9 || metrics.UnintendedRowRate != 0 || metrics.TakeoverSuccessRate != 1 ||
		metrics.CrashRecoveryRate != 1 || metrics.DeterministicExportRate != 1 {
		t.Fatalf("aggregate metrics = %#v", metrics)
	}
	if report.Hash == "" || report.Dimensions != 8 {
		t.Fatalf("report identity = %#v", report)
	}
	replayed, err := benchmark.NewRunner(adapter).Run(context.Background(), suite)
	if err != nil || replayed.Hash != report.Hash {
		t.Fatalf("deterministic replay hash = %q, %v; want %q", replayed.Hash, err, report.Hash)
	}
}

func TestSuiteAndOutcomesRejectIncompleteOrFabricatedEvidence(t *testing.T) {
	t.Parallel()

	invalid := []byte(`{"version":"memora.ai-benchmark/v1","id":"bad","scenarios":[{"id":"x","kind":"revision","hosts":[],"turns":[]}]}`)
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := osWriteFile(path, invalid); err != nil {
		t.Fatal(err)
	}
	if _, err := benchmark.Load(path); err == nil {
		t.Fatal("Load() error = nil, want incomplete dataset rejection")
	}
	suite := benchmark.Suite{Version: benchmark.SuiteVersion, ID: "bad-outcome", Scenarios: []benchmark.Scenario{{
		ID: "x", Kind: benchmark.KindRevision, Hosts: []string{"codex"}, Turns: []benchmark.Turn{{ID: "t", Intent: "revise", Expected: "revision"}},
	}}}
	adapter := benchmark.NewScriptedAdapter("fabricated", map[string]benchmark.Outcome{"x": {
		Counts: benchmark.Counts{WritesCorrect: 2, WritesAttempted: 1},
	}})
	if _, err := benchmark.NewRunner(adapter).Run(context.Background(), suite); err == nil {
		t.Fatal("Run() error = nil, want impossible counter rejection")
	}
}

func TestBaselineRegistryExposesComparableAdapters(t *testing.T) {
	t.Parallel()

	want := []string{"no-memory", "markdown-search", "sqlite-fts", "vector", "memora"}
	got := benchmark.BaselineNames()
	encodedGot, _ := json.Marshal(got)
	encodedWant, _ := json.Marshal(want)
	if string(encodedGot) != string(encodedWant) {
		t.Fatalf("baseline names = %s, want %s", encodedGot, encodedWant)
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

func osWriteFile(path string, content []byte) error {
	return os.WriteFile(path, content, 0o600)
}
