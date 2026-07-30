package benchmark_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestReleaseCorpusFixesScoredRecordsQueriesAndNegativeExamples(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	suite, err := benchmark.Load(filepath.Join(root, "benchmarks", "ai-native-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := benchmark.LoadReleaseCorpus(filepath.Join(root, "benchmarks", "ai-native-release-v0.json"))
	if err != nil {
		t.Fatal(err)
	}
	scenarios := map[string]bool{}
	for _, scenario := range suite.Scenarios {
		scenarios[scenario.ID] = true
	}
	durable, rejected, takeover := 0, 0, 0
	for _, record := range corpus.Records {
		if !scenarios[record.ScenarioID] {
			t.Errorf("record %q scenario %q is not in canonical suite", record.ID, record.ScenarioID)
		}
		if record.Durable {
			durable++
		} else {
			rejected++
		}
	}
	for _, query := range corpus.Queries {
		if !scenarios[query.ScenarioID] {
			t.Errorf("query %q scenario %q is not in canonical suite", query.ID, query.ScenarioID)
		}
		if query.Takeover {
			takeover++
		}
	}
	if durable != 20 || rejected != 8 || len(corpus.Queries) != 18 || takeover != 4 {
		t.Fatalf("corpus counts durable=%d rejected=%d queries=%d takeover=%d", durable, rejected, len(corpus.Queries), takeover)
	}
}

func TestCheckedInReleaseBenchmarkPassesAndRejectsTampering(t *testing.T) {
	t.Parallel()

	path := filepath.Join(repositoryRoot(t), "benchmarks", "reports", "ai-native-release-v0.json")
	release, err := benchmark.LoadReleaseBenchmark(path)
	if err != nil {
		t.Fatal(err)
	}
	if release.Gate.Status != "passed" || len(release.Reports) != len(benchmark.BaselineNames()) {
		t.Fatalf("release gate = %#v", release.Gate)
	}
	encoded, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Bearer ", "api_key", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "482193"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("release report contains forbidden credential material %q", forbidden)
		}
	}
	release.Reports[len(release.Reports)-1].Aggregate.RecallAt5 = 0
	tampered, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	tamperedPath := filepath.Join(t.TempDir(), "tampered.json")
	if err := osWriteFile(tamperedPath, tampered); err != nil {
		t.Fatal(err)
	}
	if _, err := benchmark.LoadReleaseBenchmark(tamperedPath); err == nil {
		t.Fatal("LoadReleaseBenchmark() error = nil, want tamper rejection")
	}
}

func TestReleaseBenchmarkExecutesEveryAdapterFromObservedModelOutput(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	suite, err := benchmark.Load(filepath.Join(root, "benchmarks", "ai-native-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := benchmark.LoadReleaseCorpus(filepath.Join(root, "benchmarks", "ai-native-release-v0.json"))
	if err != nil {
		t.Fatal(err)
	}
	recordTerms := map[string][]string{}
	for _, query := range corpus.Queries {
		for _, recordID := range query.RelevantIDs {
			recordTerms[recordID] = append(recordTerms[recordID], query.Text)
		}
	}
	snapshot := benchmark.ModelSnapshot{}
	for _, record := range corpus.Records {
		if record.Durable {
			terms := append([]string{record.Text}, recordTerms[record.ID]...)
			snapshot.Records = append(snapshot.Records, benchmark.ModeledRecord{ID: record.ID, Durable: true, Terms: terms})
		}
	}
	for _, query := range corpus.Queries {
		snapshot.Queries = append(snapshot.Queries, benchmark.ModeledQuery{ID: query.ID, Terms: []string{query.Text}})
	}
	encodedCorpus, err := json.Marshal(corpus)
	if err != nil {
		t.Fatal(err)
	}
	corpusSum := sha256.Sum256(encodedCorpus)
	snapshot.Evidence = benchmark.Evidence{
		Mode: "live-model", Protocol: "test", Model: "static",
		OutputDigest: "observed", CorpusDigest: hex.EncodeToString(corpusSum[:]),
		Implementation: "test-modeler/v1",
	}
	release, err := benchmark.RunReleaseBenchmark(context.Background(), suite, corpus, staticModeler{snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if len(release.Reports) != len(benchmark.BaselineNames()) {
		t.Fatalf("reports = %d", len(release.Reports))
	}
	var memora benchmark.Report
	for _, report := range release.Reports {
		if report.Adapter == "memora" {
			memora = report
		}
	}
	if memora.Aggregate.WritePrecision != 1 || memora.Aggregate.WriteRecall != 1 ||
		memora.Aggregate.RecallAt5 < 0.9 || memora.Evidence == nil || memora.Evidence.ExecutionDigest == "" {
		t.Fatalf("memora observed report = %#v", memora)
	}
}

type staticModeler struct {
	snapshot benchmark.ModelSnapshot
}

func (modeler staticModeler) Model(context.Context, benchmark.ReleaseCorpus) (benchmark.ModelSnapshot, error) {
	return modeler.snapshot, nil
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
