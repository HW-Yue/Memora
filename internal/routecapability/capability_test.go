package routecapability_test

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/hostcontract"
	"github.com/HW-Yue/Memora/internal/routebenchmark"
	"github.com/HW-Yue/Memora/internal/routebenchmarkrunner"
	"github.com/HW-Yue/Memora/internal/routecapability"
)

const (
	skillDigest    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	protocolDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func TestBuildSlicesEveryDimensionAndSelectsCommonSafeArm(t *testing.T) {
	t.Parallel()

	corpus, contract, contractDigest, source := sourceReport(t, "")
	price := priceCard(t)
	report, err := routecapability.Build(corpus, contract, contractDigest,
		[]routebenchmarkrunner.Report{source}, &price)
	if err != nil {
		t.Fatal(err)
	}
	if report.Decision.DefaultArm != routebenchmarkrunner.ArmHybridPrefetch ||
		report.Decision.PolicyVersion != routecapability.PolicyVersion {
		t.Fatalf("default decision = %#v", report.Decision)
	}
	wantSlices := map[string]bool{
		"aggregate": false, "fanout": false, "depth": false,
		"difficulty": false, "language": false, "host_model": false,
	}
	for _, bucket := range report.Buckets {
		wantSlices[bucket.Key.Slice] = true
		if bucket.Key.Slice == "aggregate" && bucket.Key.Arm == routebenchmarkrunner.ArmHybridPrefetch {
			if bucket.Metrics.RowIDSuccess.Value != 1 || bucket.Metrics.RowIDSuccess.Lower95 <= 0.9 ||
				bucket.Delta.ModelCallsSavedPerRun <= 0 || !bucket.Cost.Available ||
				bucket.Latency.EndToEnd.P95 > aggregateBucket(t, report, routebenchmarkrunner.ArmRouter).Latency.EndToEnd.P95 {
				t.Fatalf("optimized aggregate = %#v", bucket)
			}
		}
	}
	for name, found := range wantSlices {
		if !found {
			t.Errorf("slice %q is missing", name)
		}
	}
	if len(report.SourceReports) != 1 || report.SourceReports[0].Hash != source.Hash ||
		report.PriceCardDigest != price.Hash || len(report.Hash) != 64 {
		t.Fatalf("report identity = %#v", report)
	}
	if err := report.ValidateAgainst(corpus, contract, contractDigest,
		[]routebenchmarkrunner.Report{source}, &price); err != nil {
		t.Fatalf("revalidate report: %v", err)
	}
}

func TestDefaultRejectsAnyMatchedRowRegressionAndMissingPriceStaysUnavailable(t *testing.T) {
	t.Parallel()

	corpus, contract, contractDigest, source := sourceReport(t, "hybrid_prefetch")
	report, err := routecapability.Build(corpus, contract, contractDigest,
		[]routebenchmarkrunner.Report{source}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Decision.DefaultArm != routebenchmarkrunner.ArmHybrid {
		t.Fatalf("unsafe speculative arm decision = %#v", report.Decision)
	}
	evaluation := decisionFor(t, report, routebenchmarkrunner.ArmHybridPrefetch)
	if evaluation.Eligible || !contains(evaluation.Reasons, "matched_rowid_regression") {
		t.Fatalf("unsafe arm evaluation = %#v", evaluation)
	}
	for _, bucket := range report.Buckets {
		if bucket.Cost.Available || bucket.Cost.TotalMicroUSD != 0 {
			t.Fatalf("missing price fabricated cost: %#v", bucket.Cost)
		}
	}
}

func TestBuildRejectsDuplicateSourceRunsAndStrictReportTampering(t *testing.T) {
	t.Parallel()

	corpus, contract, contractDigest, source := sourceReport(t, "")
	if _, err := routecapability.Build(corpus, contract, contractDigest,
		[]routebenchmarkrunner.Report{source, source}, nil); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate source error = %v", err)
	}
	report, err := routecapability.Build(corpus, contract, contractDigest,
		[]routebenchmarkrunner.Report{source}, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := routecapability.Encode(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := routecapability.Decode(encoded); err != nil {
		t.Fatal(err)
	}
	unknown := strings.Replace(string(encoded), "{", `{"unknown":true,`, 1)
	if _, err := routecapability.Decode([]byte(unknown)); err == nil {
		t.Fatal("unknown report field was accepted")
	}
	tampered := strings.Replace(string(encoded), `"default_arm": "hybrid_prefetch"`, `"default_arm": "router"`, 1)
	if _, err := routecapability.Decode([]byte(tampered)); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("tampered report error = %v", err)
	}
}

type expected struct {
	match bool
	path  []string
	rowID string
	roots []string
}

type capabilityDriver struct {
	expected    map[string]expected
	degradeArm  routebenchmarkrunner.Arm
	degradedOne bool
}

func (driver *capabilityDriver) Run(_ context.Context, request routebenchmarkrunner.Request) (routebenchmarkrunner.Observation, error) {
	want := driver.expected[request.Task.ID]
	choices, selected, facts := append([]string(nil), want.path...), want.rowID, []string{}
	if want.match {
		facts = []string{want.rowID}
	}
	if request.Arm == driver.degradeArm && want.match && !driver.degradedOne {
		driver.degradedOne = true
		selected = ""
		facts = []string{}
	}
	modelCalls := map[routebenchmarkrunner.Arm]uint64{
		routebenchmarkrunner.ArmRouter: 10, routebenchmarkrunner.ArmLexical: 9,
		routebenchmarkrunner.ArmVector: 8, routebenchmarkrunner.ArmHybrid: 7,
		routebenchmarkrunner.ArmHybridPrefetch: 6,
	}[request.Arm]
	predictors, prefetched := []string{}, []string{}
	if request.Arm != routebenchmarkrunner.ArmRouter && want.match {
		predictors = []string{want.path[len(want.path)-1]}
	}
	if request.Arm == routebenchmarkrunner.ArmHybridPrefetch && want.match {
		prefetched = append([]string(nil), want.roots...)
	}
	return routebenchmarkrunner.Observation{
		Version: routebenchmarkrunner.ObservationVersion, RunID: request.RunID,
		TaskDigest: request.TaskDigest, SkillDigest: request.SkillDigest,
		ProtocolDigest: request.ProtocolDigest, CorpusDigest: request.CorpusDigest,
		Snapshot: request.Fixture.Snapshot, Arm: request.Arm,
		Receipt: hostcontract.Receipt{
			Version: hostcontract.ReceiptVersion, TaskDigest: request.TaskDigest,
			SkillDigest: request.SkillDigest, ProtocolDigest: request.ProtocolDigest,
			Host: request.Host, Status: "completed", AnswerDigest: strings.Repeat("d", 64),
			ToolCalls: []hostcontract.ToolCall{{Sequence: 1, Command: "query",
				ResultDigest: strings.Repeat("e", 64), ContextCharacters: 100, ElapsedMillis: 5}},
			Usage: hostcontract.Usage{InputTokens: modelCalls * 10, OutputTokens: modelCalls * 2,
				ContextCharacters: 100, ElapsedMillis: modelCalls * 10},
		},
		LevelChoices: choices, SelectedRowID: selected, FactReadRowIDs: facts,
		PredictorRouteIDs: predictors, PrefetchedRootRouteIDs: prefetched,
		ModelCalls: modelCalls, Timing: routebenchmarkrunner.Timing{
			MSQLMillis: 5, ModelMillis: modelCalls * 8, EndToEndMillis: modelCalls * 10,
		},
		Vector: routebenchmarkrunner.VectorCost{GenerationBytes: 4096, RebuildMillis: 3, PeakMemoryBytes: 8192},
	}, nil
}

func sourceReport(t *testing.T, degrade string) (
	routebenchmark.Corpus, hostcontract.Contract, string, routebenchmarkrunner.Report,
) {
	t.Helper()
	corpus, err := routebenchmark.Generate()
	if err != nil {
		t.Fatal(err)
	}
	contract, digest := canonicalContract(t)
	expectedByID := map[string]expected{}
	for _, scenario := range corpus.Scenarios {
		expectedByID[scenario.ID] = expected{scenario.Expected.Match,
			append([]string(nil), scenario.Expected.Path...), scenario.Expected.RowID,
			append([]string(nil), scenario.Table.RootRouteIDs...)}
	}
	driver := &capabilityDriver{expected: expectedByID, degradeArm: routebenchmarkrunner.Arm(degrade)}
	profiles := []routebenchmarkrunner.Profile{
		{Host: hostcontract.HostProfile{Surface: "codex", Provider: "openai", Protocol: "openai-compatible", Model: "gpt-live", EndpointHost: "openai.example"}, Driver: driver},
		{Host: hostcontract.HostProfile{Surface: "claude-code", Provider: "anthropic", Protocol: "anthropic-compatible", Model: "claude-live", EndpointHost: "anthropic.example"}, Driver: driver},
		{Host: hostcontract.HostProfile{Surface: "codex", Provider: "kimi", Protocol: "openai-compatible", Model: "kimi-live", EndpointHost: "kimi.example"}, Driver: driver},
	}
	runner, err := routebenchmarkrunner.New(routebenchmarkrunner.Options{
		Contract: contract, ContractDigest: digest,
		SkillDigest: skillDigest, ProtocolDigest: protocolDigest, Profiles: profiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), corpus)
	if err != nil {
		t.Fatal(err)
	}
	return corpus, contract, digest, report
}

func priceCard(t *testing.T) routecapability.PriceCard {
	t.Helper()
	card := routecapability.PriceCard{
		Version: routecapability.PriceCardVersion, ID: "test-2026-08",
		Entries: []routecapability.Price{
			{Provider: "anthropic", Model: "claude-live", InputMicroUSDPerMillion: 3_000_000, OutputMicroUSDPerMillion: 15_000_000},
			{Provider: "kimi", Model: "kimi-live", InputMicroUSDPerMillion: 1_000_000, OutputMicroUSDPerMillion: 3_000_000},
			{Provider: "openai", Model: "gpt-live", InputMicroUSDPerMillion: 2_000_000, OutputMicroUSDPerMillion: 8_000_000},
		},
	}
	if err := card.Seal(); err != nil {
		t.Fatal(err)
	}
	return card
}

func canonicalContract(t *testing.T) (hostcontract.Contract, string) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	contract, digest, err := hostcontract.Load(filepath.Join(root, "skills", "memora", "host-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	return contract, digest
}

func aggregateBucket(t *testing.T, report routecapability.Report, arm routebenchmarkrunner.Arm) routecapability.Bucket {
	t.Helper()
	for _, bucket := range report.Buckets {
		if bucket.Key.Slice == "aggregate" && bucket.Key.Arm == arm {
			return bucket
		}
	}
	t.Fatalf("aggregate bucket for %s not found", arm)
	return routecapability.Bucket{}
}

func decisionFor(t *testing.T, report routecapability.Report, arm routebenchmarkrunner.Arm) routecapability.ArmDecision {
	t.Helper()
	for _, decision := range report.Decision.Arms {
		if decision.Arm == arm {
			return decision
		}
	}
	t.Fatalf("decision for %s not found", arm)
	return routecapability.ArmDecision{}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
