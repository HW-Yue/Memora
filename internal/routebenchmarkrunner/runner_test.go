package routebenchmarkrunner_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/hostcontract"
	"github.com/HW-Yue/Memora/internal/routebenchmark"
	"github.com/HW-Yue/Memora/internal/routebenchmarkrunner"
)

const (
	testSkillDigest    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testProtocolDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func TestRunnerExecutesBlindCompleteMatrixAndScoresGroundTruth(t *testing.T) {
	t.Parallel()

	corpus, err := routebenchmark.Generate()
	if err != nil {
		t.Fatal(err)
	}
	contract, contractDigest, err := loadContract(t)
	if err != nil {
		t.Fatal(err)
	}
	expected := expectedByScenario(corpus)
	driver := &perfectDriver{expected: expected}
	runner, err := routebenchmarkrunner.New(routebenchmarkrunner.Options{
		Contract: contract, ContractDigest: contractDigest,
		SkillDigest: testSkillDigest, ProtocolDigest: testProtocolDigest,
		Profiles: profiles(driver),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), corpus)
	if err != nil {
		t.Fatal(err)
	}
	wantRuns := len(corpus.Scenarios) * len(routebenchmarkrunner.Arms()) * 3
	if len(report.Runs) != wantRuns || report.Counts.Runs != uint64(wantRuns) ||
		report.Counts.Completed != uint64(wantRuns) {
		t.Fatalf("run coverage = %d / %#v, want %d", len(report.Runs), report.Counts, wantRuns)
	}
	if report.Metrics.LevelTop1Accuracy != 1 || report.Metrics.ExactPathSuccess != 1 ||
		report.Metrics.RowIDSuccess != 1 || report.Metrics.PredictorRecallAtK != 1 ||
		report.Metrics.PrefetchHitRate != 1 || len(report.Hash) != 64 {
		t.Fatalf("perfect metrics/report = %#v / %q", report.Metrics, report.Hash)
	}
	if err := report.ValidateAgainst(corpus, contract, contractDigest); err != nil {
		t.Fatalf("validate against frozen evidence: %v", err)
	}
	if driver.calls != wantRuns {
		t.Fatalf("driver calls = %d, want %d", driver.calls, wantRuns)
	}
	for _, request := range driver.requests {
		if request.Fixture.Snapshot == "" || request.Task.Prompt == "" ||
			request.Task.CorpusDigest != corpus.Hash {
			t.Fatalf("blind request identity = %#v", request)
		}
	}
	for index, run := range report.Runs {
		if run.RunID == "" || run.TaskDigest == "" || run.ScenarioID == "" ||
			run.Observation.Receipt.AnswerDigest == "" {
			t.Fatalf("run %d identity = %#v", index, run)
		}
		if index > 0 && report.Runs[index-1].RunID == run.RunID {
			t.Fatalf("run ID duplicated at %d", index)
		}
	}
}

func TestCommandDriverUsesStrictBoundedJSONAndTaskTimeout(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEMORA_ROUTE_DRIVER_HELPER", "success")
	driver, err := routebenchmarkrunner.NewCommandDriver(executable,
		[]string{"-test.run=TestRouteDriverHelperProcess", "--", "literal;not-a-shell"})
	if err != nil {
		t.Fatal(err)
	}
	request := routebenchmarkrunner.Request{
		Version: routebenchmarkrunner.RequestVersion, RunID: strings.Repeat("a", 64),
		Task: hostcontract.Task{Budget: hostcontract.Budget{MaxElapsedMillis: 10000}},
	}
	observation, err := driver.Run(context.Background(), request)
	if err != nil || observation.RunID != request.RunID || observation.Version != routebenchmarkrunner.ObservationVersion {
		t.Fatalf("command observation = %#v, %v", observation, err)
	}

	t.Setenv("MEMORA_ROUTE_DRIVER_HELPER", "unknown")
	if _, err := driver.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("unknown output error = %v", err)
	}
	t.Setenv("MEMORA_ROUTE_DRIVER_HELPER", "timeout")
	request.Task.Budget.MaxElapsedMillis = 100
	if _, err := driver.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "did not complete") {
		t.Fatalf("timeout error = %v", err)
	}
	t.Setenv("MEMORA_ROUTE_DRIVER_HELPER", "overflow")
	request.Task.Budget.MaxElapsedMillis = 10000
	if _, err := driver.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("overflow error = %v", err)
	}
	t.Setenv("MEMORA_ROUTE_DRIVER_HELPER", "secret-error")
	if _, err := driver.Run(context.Background(), request); err == nil ||
		strings.Contains(err.Error(), "do-not-leak-this-token") || !strings.Contains(err.Error(), "stderr_bytes") {
		t.Fatalf("redacted process error = %v", err)
	}
	if _, err := routebenchmarkrunner.NewCommandDriver(executable,
		[]string{"--api-key=must-not-enter-config"}); err == nil {
		t.Fatal("credential-bearing driver argument was accepted")
	}
	if _, err := routebenchmarkrunner.NewCommandDriver(executable,
		[]string{"--endpoint=https://provider.example/v1"}); err == nil {
		t.Fatal("full endpoint URL was accepted")
	}
}

func TestRouteDriverHelperProcess(t *testing.T) {
	mode := os.Getenv("MEMORA_ROUTE_DRIVER_HELPER")
	if mode == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(90)
	}
	var request routebenchmarkrunner.Request
	if json.Unmarshal(scanner.Bytes(), &request) != nil {
		os.Exit(91)
	}
	if mode == "timeout" {
		time.Sleep(250 * time.Millisecond)
		return
	}
	if mode == "overflow" {
		_, _ = os.Stdout.Write(make([]byte, 2<<20+1))
		return
	}
	if mode == "secret-error" {
		fmt.Fprint(os.Stderr, "do-not-leak-this-token")
		os.Exit(92)
	}
	if mode == "unknown" {
		fmt.Printf(`{"unknown":true,"version":%q,"run_id":%q}`,
			routebenchmarkrunner.ObservationVersion, request.RunID)
		return
	}
	encoded, _ := json.Marshal(routebenchmarkrunner.Observation{
		Version: routebenchmarkrunner.ObservationVersion, RunID: request.RunID,
	})
	_, _ = os.Stdout.Write(encoded)
	os.Exit(0)
}

func TestDriverConfigCanonicalDigestsAndAtomicReport(t *testing.T) {
	contract, _, err := loadContract(t)
	if err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillDigest, protocolDigest, err := routebenchmarkrunner.CanonicalDigests(filepath.Join(root, "skills", "memora"))
	if err != nil || len(skillDigest) != 64 || len(protocolDigest) != 64 {
		t.Fatalf("canonical digests = %q/%q, %v", skillDigest, protocolDigest, err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := routebenchmarkrunner.DriverConfig{Version: routebenchmarkrunner.DriverConfigVersion}
	for _, profile := range profiles(&perfectDriver{}) {
		config.Profiles = append(config.Profiles, routebenchmarkrunner.CommandProfileConfig{
			Host: profile.Host, Executable: executable, Arguments: []string{},
		})
	}
	if err := config.Validate(contract); err != nil {
		t.Fatal(err)
	}
	built, err := config.BuildProfiles()
	if err != nil || len(built) != 3 {
		t.Fatalf("built profiles = %d, %v", len(built), err)
	}
	report := buildPerfectReport(t)
	path := filepath.Join(t.TempDir(), "reports", "route.json")
	if err := routebenchmarkrunner.WriteAtomic(path, report); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("report mode = %v", info.Mode())
	}
	if _, err := routebenchmarkrunner.Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerKeepsQualityAndSafetyFailuresButRejectsForgedReceipt(t *testing.T) {
	t.Parallel()

	corpus, err := routebenchmark.Generate()
	if err != nil {
		t.Fatal(err)
	}
	contract, contractDigest, err := loadContract(t)
	if err != nil {
		t.Fatal(err)
	}
	badQuality := &perfectDriver{expected: expectedByScenario(corpus), alter: func(response *routebenchmarkrunner.Observation) {
		response.LevelChoices = []string{"route_ffffffffffffffffffffffffffffffff"}
		response.SelectedRowID = "row_ffffffffffffffffffffffffffffffff"
		response.FactReadRowIDs = []string{"row_ffffffffffffffffffffffffffffffff"}
		response.PermissionDenials = 1
		response.Truncations = 1
	}}
	runner, err := routebenchmarkrunner.New(routebenchmarkrunner.Options{
		Contract: contract, ContractDigest: contractDigest,
		SkillDigest: testSkillDigest, ProtocolDigest: testProtocolDigest,
		Profiles: profiles(badQuality),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), corpus)
	if err != nil {
		t.Fatal(err)
	}
	if report.Metrics.RowIDSuccess != 0 || report.Counts.UnauthorizedLocators == 0 ||
		report.Counts.WrongFactReads == 0 || report.Counts.PermissionDenials == 0 ||
		report.Counts.Truncations == 0 || len(report.Failures) == 0 {
		t.Fatalf("quality/safety evidence was hidden: %#v %#v", report.Counts, report.Failures)
	}

	forged := &perfectDriver{expected: expectedByScenario(corpus), alter: func(response *routebenchmarkrunner.Observation) {
		response.Receipt.TaskDigest = strings.Repeat("d", 64)
	}}
	runner, err = routebenchmarkrunner.New(routebenchmarkrunner.Options{
		Contract: contract, ContractDigest: contractDigest,
		SkillDigest: testSkillDigest, ProtocolDigest: testProtocolDigest,
		Profiles: profiles(forged),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), corpus); err == nil || !strings.Contains(err.Error(), "receipt") {
		t.Fatalf("forged receipt error = %v", err)
	}
}

func TestReportStrictReloadRejectsUnknownFieldsAndTampering(t *testing.T) {
	t.Parallel()

	report := buildPerfectReport(t)
	encoded, err := routebenchmarkrunner.Encode(report)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := routebenchmarkrunner.Decode(encoded)
	if err != nil || loaded.Hash != report.Hash {
		t.Fatalf("reload = %#v, %v", loaded, err)
	}
	unknown := strings.Replace(string(encoded), "{", `{"unknown":true,`, 1)
	if _, err := routebenchmarkrunner.Decode([]byte(unknown)); err == nil {
		t.Fatal("unknown report field was accepted")
	}
	tampered := strings.Replace(string(encoded), `"corpus_id": "route-retrieval-v1"`, `"corpus_id": "tampered"`, 1)
	if _, err := routebenchmarkrunner.Decode([]byte(tampered)); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("tampered report error = %v", err)
	}
}

type expectedResult struct {
	match bool
	path  []string
	rowID string
	roots []string
}

type perfectDriver struct {
	expected map[string]expectedResult
	alter    func(*routebenchmarkrunner.Observation)
	calls    int
	requests []routebenchmarkrunner.Request
}

func (driver *perfectDriver) Run(_ context.Context, request routebenchmarkrunner.Request) (routebenchmarkrunner.Observation, error) {
	driver.calls++
	driver.requests = append(driver.requests, request)
	want := driver.expected[request.Task.ID]
	choices, selected, facts := append([]string(nil), want.path...), want.rowID, []string{}
	if want.match {
		facts = []string{want.rowID}
	}
	predictors, prefetched := []string{}, []string{}
	if request.Arm != routebenchmarkrunner.ArmRouter && want.match {
		predictors = []string{want.path[len(want.path)-1]}
	}
	if request.Arm == routebenchmarkrunner.ArmHybridPrefetch && want.match {
		prefetched = append([]string(nil), want.roots...)
	}
	observation := routebenchmarkrunner.Observation{
		Version: routebenchmarkrunner.ObservationVersion, RunID: request.RunID,
		TaskDigest: request.TaskDigest, SkillDigest: request.SkillDigest,
		ProtocolDigest: request.ProtocolDigest, CorpusDigest: request.CorpusDigest,
		Snapshot: request.Fixture.Snapshot, Arm: request.Arm,
		Receipt: hostcontract.Receipt{
			Version: hostcontract.ReceiptVersion, TaskDigest: request.TaskDigest,
			SkillDigest: request.SkillDigest, ProtocolDigest: request.ProtocolDigest,
			Host: request.Host, Status: "completed", AnswerDigest: strings.Repeat("e", 64),
			ToolCalls: []hostcontract.ToolCall{{Sequence: 1, Command: "query",
				ResultDigest: strings.Repeat("f", 64), ContextCharacters: 100, ElapsedMillis: 2}},
			Usage: hostcontract.Usage{InputTokens: 20, OutputTokens: 5,
				ContextCharacters: 100, ElapsedMillis: 5},
		},
		LevelChoices: choices, SelectedRowID: selected, FactReadRowIDs: facts,
		PredictorRouteIDs: predictors, PrefetchedRootRouteIDs: prefetched,
		ModelCalls: uint64(len(choices) + 1), FallbackCalls: 0,
		Timing: routebenchmarkrunner.Timing{MSQLMillis: 1, ModelMillis: 4, EndToEndMillis: 5},
	}
	if driver.alter != nil {
		driver.alter(&observation)
	}
	return observation, nil
}

func profiles(driver routebenchmarkrunner.Driver) []routebenchmarkrunner.Profile {
	return []routebenchmarkrunner.Profile{
		{Host: hostcontract.HostProfile{Surface: "codex", Provider: "openai", Protocol: "openai-compatible", Model: "gpt-test", EndpointHost: "provider.example"}, Driver: driver},
		{Host: hostcontract.HostProfile{Surface: "claude-code", Provider: "anthropic", Protocol: "anthropic-compatible", Model: "claude-test", EndpointHost: "provider.example"}, Driver: driver},
		{Host: hostcontract.HostProfile{Surface: "codex", Provider: "kimi", Protocol: "openai-compatible", Model: "kimi-test", EndpointHost: "provider.example"}, Driver: driver},
	}
}

func expectedByScenario(corpus routebenchmark.Corpus) map[string]expectedResult {
	result := make(map[string]expectedResult, len(corpus.Scenarios))
	for _, scenario := range corpus.Scenarios {
		result[scenario.ID] = expectedResult{match: scenario.Expected.Match,
			path: append([]string(nil), scenario.Expected.Path...), rowID: scenario.Expected.RowID,
			roots: append([]string(nil), scenario.Table.RootRouteIDs...)}
	}
	return result
}

func loadContract(t *testing.T) (hostcontract.Contract, string, error) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return hostcontract.Load(filepath.Join(root, "skills", "memora", "host-contract.json"))
}

func buildPerfectReport(t *testing.T) routebenchmarkrunner.Report {
	t.Helper()
	corpus, err := routebenchmark.Generate()
	if err != nil {
		t.Fatal(err)
	}
	contract, contractDigest, err := loadContract(t)
	if err != nil {
		t.Fatal(err)
	}
	driver := &perfectDriver{expected: expectedByScenario(corpus)}
	runner, err := routebenchmarkrunner.New(routebenchmarkrunner.Options{
		Contract: contract, ContractDigest: contractDigest,
		SkillDigest: testSkillDigest, ProtocolDigest: testProtocolDigest,
		Profiles: profiles(driver),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), corpus)
	if err != nil {
		t.Fatal(err)
	}
	return report
}
