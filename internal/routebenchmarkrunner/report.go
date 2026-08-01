package routebenchmarkrunner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"reflect"

	"github.com/HW-Yue/Memora/internal/hostcontract"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routebenchmark"
)

type RunScore struct {
	Completed            bool   `json:"completed"`
	LevelsExpected       uint64 `json:"levels_expected"`
	LevelTop1Correct     uint64 `json:"level_top1_correct"`
	ExactPathSuccess     bool   `json:"exact_path_success"`
	RowIDSuccess         bool   `json:"rowid_success"`
	PredictorEligible    bool   `json:"predictor_eligible"`
	PredictorHit         bool   `json:"predictor_hit"`
	PrefetchEligible     bool   `json:"prefetch_eligible"`
	PrefetchHit          bool   `json:"prefetch_hit"`
	IrrelevantLocators   uint64 `json:"irrelevant_locators"`
	UnauthorizedLocators uint64 `json:"unauthorized_locators"`
	WrongFactReads       uint64 `json:"wrong_fact_reads"`
}

type RunReport struct {
	RunID       string                   `json:"run_id"`
	ScenarioID  string                   `json:"scenario_id"`
	TaskDigest  string                   `json:"task_digest"`
	Snapshot    string                   `json:"snapshot"`
	Topic       string                   `json:"topic"`
	Difficulty  string                   `json:"difficulty"`
	Language    string                   `json:"language"`
	Fanout      int                      `json:"fanout"`
	Depth       int                      `json:"depth"`
	Arm         Arm                      `json:"arm"`
	Host        hostcontract.HostProfile `json:"host"`
	Observation Observation              `json:"observation"`
	Score       RunScore                 `json:"score"`
}

type Counts struct {
	Runs                 uint64 `json:"runs"`
	Completed            uint64 `json:"completed"`
	Failed               uint64 `json:"failed"`
	Cancelled            uint64 `json:"cancelled"`
	BudgetExhausted      uint64 `json:"budget_exhausted"`
	LevelsExpected       uint64 `json:"levels_expected"`
	LevelTop1Correct     uint64 `json:"level_top1_correct"`
	ExactPathSuccess     uint64 `json:"exact_path_success"`
	RowIDSuccess         uint64 `json:"rowid_success"`
	PredictorCases       uint64 `json:"predictor_cases"`
	PredictorHits        uint64 `json:"predictor_hits"`
	PrefetchCases        uint64 `json:"prefetch_cases"`
	PrefetchHits         uint64 `json:"prefetch_hits"`
	FallbackCalls        uint64 `json:"fallback_calls"`
	ModelCalls           uint64 `json:"model_calls"`
	ToolCalls            uint64 `json:"tool_calls"`
	InputTokens          uint64 `json:"input_tokens"`
	OutputTokens         uint64 `json:"output_tokens"`
	MispredictTokens     uint64 `json:"mispredict_tokens"`
	ContextCharacters    uint64 `json:"context_characters"`
	IrrelevantLocators   uint64 `json:"irrelevant_locators"`
	UnauthorizedLocators uint64 `json:"unauthorized_locators"`
	WrongFactReads       uint64 `json:"wrong_fact_reads"`
	PermissionDenials    uint64 `json:"permission_denials"`
	Truncations          uint64 `json:"truncations"`
}

type Metrics struct {
	LevelTop1Accuracy  float64 `json:"level_top1_accuracy"`
	ExactPathSuccess   float64 `json:"exact_path_success"`
	RowIDSuccess       float64 `json:"rowid_success"`
	PredictorRecallAtK float64 `json:"predictor_recall_at_k"`
	PrefetchHitRate    float64 `json:"prefetch_hit_rate"`
	MeanFallbackCalls  float64 `json:"mean_fallback_calls"`
	MeanModelCalls     float64 `json:"mean_model_calls"`
	MeanInputTokens    float64 `json:"mean_input_tokens"`
	MeanOutputTokens   float64 `json:"mean_output_tokens"`
}

type Failure struct {
	RunID string `json:"run_id"`
	Code  string `json:"code"`
}

type Report struct {
	Version        string                     `json:"version"`
	Runner         string                     `json:"runner"`
	CorpusID       string                     `json:"corpus_id"`
	CorpusDigest   string                     `json:"corpus_digest"`
	ContractDigest string                     `json:"contract_digest"`
	SkillDigest    string                     `json:"skill_digest"`
	ProtocolDigest string                     `json:"protocol_digest"`
	Arms           []Arm                      `json:"arms"`
	Profiles       []hostcontract.HostProfile `json:"profiles"`
	Runs           []RunReport                `json:"runs"`
	Counts         Counts                     `json:"counts"`
	Metrics        Metrics                    `json:"metrics"`
	Failures       []Failure                  `json:"failures"`
	Hash           string                     `json:"hash"`
}

func (report *Report) Seal() error {
	if report == nil {
		return runnerError(result.CodeInternal, "report is nil")
	}
	if report.Version != ReportVersion || report.Runner != RunnerVersion || report.CorpusID == "" ||
		!validDigest(report.CorpusDigest) || !validDigest(report.ContractDigest) ||
		!validDigest(report.SkillDigest) || !validDigest(report.ProtocolDigest) ||
		!equalArms(report.Arms, canonicalArms) || !validReportMatrixShape(*report) {
		return runnerError(result.CodeValidation, "report identity, arms, profiles, and runs are required")
	}
	recompute(report)
	hash, err := reportDigest(*report)
	if err != nil {
		return err
	}
	report.Hash = hash
	return nil
}

func (report Report) Validate() error {
	if !validDigest(report.Hash) {
		return runnerError(result.CodeValidation, "report hash is invalid")
	}
	wantHash, err := reportDigest(report)
	if err != nil {
		return err
	}
	if report.Hash != wantHash {
		return runnerError(result.CodeValidation, "report hash mismatch")
	}
	want := report
	want.Hash = ""
	recompute(&want)
	if report.Version != ReportVersion || report.Runner != RunnerVersion || report.CorpusID == "" ||
		!validDigest(report.CorpusDigest) || !validDigest(report.ContractDigest) ||
		!validDigest(report.SkillDigest) || !validDigest(report.ProtocolDigest) ||
		!equalArms(report.Arms, canonicalArms) || !validReportMatrixShape(report) ||
		!reflect.DeepEqual(report.Counts, want.Counts) || !reflect.DeepEqual(report.Metrics, want.Metrics) ||
		!reflect.DeepEqual(report.Failures, want.Failures) {
		return runnerError(result.CodeValidation, "report content does not match run evidence")
	}
	seen := map[string]bool{}
	for _, run := range report.Runs {
		if !validDigest(run.RunID) || !validDigest(run.TaskDigest) || !validDigest(run.Snapshot) ||
			run.ScenarioID == "" || !containsArm(canonicalArms, run.Arm) ||
			!containsHost(report.Profiles, run.Host) || seen[run.RunID] {
			return runnerError(result.CodeValidation, "report run identity is invalid or duplicated")
		}
		seen[run.RunID] = true
	}
	return nil
}

func validReportMatrixShape(report Report) bool {
	if len(report.Profiles) < 3 || len(report.Runs) == 0 || report.Failures == nil {
		return false
	}
	cellWidth := len(canonicalArms) * len(report.Profiles)
	return cellWidth > 0 && len(report.Runs)%cellWidth == 0
}

func containsArm(values []Arm, target Arm) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsHost(values []hostcontract.HostProfile, target hostcontract.HostProfile) bool {
	for _, value := range values {
		if reflect.DeepEqual(value, target) {
			return true
		}
	}
	return false
}

func (report Report) ValidateAgainst(
	corpus routebenchmark.Corpus,
	contract hostcontract.Contract,
	contractDigest string,
) error {
	if err := report.Validate(); err != nil {
		return err
	}
	if err := corpus.Validate(); err != nil {
		return err
	}
	if err := contract.Validate(); err != nil {
		return err
	}
	if report.CorpusID != corpus.ID || report.CorpusDigest != corpus.Hash ||
		report.ContractDigest != contractDigest || !validDigest(contractDigest) {
		return runnerError(result.CodeValidation, "report does not bind the supplied Corpus and host contract")
	}
	wantRuns := len(corpus.Scenarios) * len(canonicalArms) * len(report.Profiles)
	if len(report.Profiles) < 3 || len(report.Runs) != wantRuns {
		return runnerError(result.CodeValidation, "report matrix is incomplete")
	}
	verifier := &Runner{options: Options{
		Contract: contract, ContractDigest: contractDigest,
		SkillDigest: report.SkillDigest, ProtocolDigest: report.ProtocolDigest,
	}}
	position := 0
	for _, scenario := range corpus.Scenarios {
		task, err := corpus.HostTask(scenario.ID)
		if err != nil {
			return err
		}
		taskDigest, err := contract.TaskDigest(task)
		if err != nil {
			return err
		}
		matrix := make([]hostcontract.Invocation, 0, len(report.Profiles))
		for _, host := range report.Profiles {
			matrix = append(matrix, hostcontract.Invocation{
				Version: hostcontract.InvocationVersion, TaskDigest: taskDigest,
				SkillDigest: report.SkillDigest, ProtocolDigest: report.ProtocolDigest, Host: host,
			})
		}
		if err := contract.ValidateMatrix(matrix); err != nil {
			return err
		}
		for _, arm := range canonicalArms {
			for _, host := range report.Profiles {
				run := report.Runs[position]
				request, err := verifier.request(corpus, scenario, task, taskDigest, arm, host)
				if err != nil {
					return err
				}
				if run.RunID != request.RunID || run.ScenarioID != scenario.ID ||
					run.TaskDigest != taskDigest || run.Snapshot != scenario.Snapshot ||
					run.Topic != scenario.Topic || run.Difficulty != scenario.Difficulty ||
					run.Language != scenario.Language || run.Fanout != scenario.Fanout ||
					run.Depth != scenario.Depth || run.Arm != arm || !reflect.DeepEqual(run.Host, host) {
					return runnerError(result.CodeValidation, "report run %d differs from the frozen matrix", position)
				}
				if err := verifier.validateObservation(task, request, run.Observation); err != nil {
					return err
				}
				wantScore, _ := scoreObservation(scenario, arm, run.Observation)
				if !reflect.DeepEqual(run.Score, wantScore) {
					return runnerError(result.CodeValidation, "report run %s score differs from ground truth", run.RunID)
				}
				position++
			}
		}
	}
	return nil
}

func Encode(report Report) ([]byte, error) {
	if err := report.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, runnerError(result.CodeInternal, "encode report: %v", err)
	}
	return append(encoded, '\n'), nil
}

func Decode(encoded []byte) (Report, error) {
	var report Report
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return Report{}, runnerError(result.CodeValidation, "decode report: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Report{}, runnerError(result.CodeValidation, "decode report: trailing JSON value")
	}
	if err := report.Validate(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func Load(path string) (Report, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return Report{}, runnerError(result.CodeInternal, "read report: %v", err)
	}
	return Decode(encoded)
}

func recompute(report *Report) {
	counts := Counts{}
	failures := []Failure{}
	for _, run := range report.Runs {
		counts.Runs++
		switch run.Observation.Receipt.Status {
		case "completed":
			counts.Completed++
		case "failed":
			counts.Failed++
		case "cancelled":
			counts.Cancelled++
		case "budget_exhausted":
			counts.BudgetExhausted++
		}
		counts.LevelsExpected += run.Score.LevelsExpected
		counts.LevelTop1Correct += run.Score.LevelTop1Correct
		counts.ExactPathSuccess += boolCount(run.Score.ExactPathSuccess)
		counts.RowIDSuccess += boolCount(run.Score.RowIDSuccess)
		counts.PredictorCases += boolCount(run.Score.PredictorEligible)
		counts.PredictorHits += boolCount(run.Score.PredictorHit)
		counts.PrefetchCases += boolCount(run.Score.PrefetchEligible)
		counts.PrefetchHits += boolCount(run.Score.PrefetchHit)
		counts.FallbackCalls += run.Observation.FallbackCalls
		counts.ModelCalls += run.Observation.ModelCalls
		counts.ToolCalls += uint64(len(run.Observation.Receipt.ToolCalls))
		counts.InputTokens += run.Observation.Receipt.Usage.InputTokens
		counts.OutputTokens += run.Observation.Receipt.Usage.OutputTokens
		counts.MispredictTokens += run.Observation.MispredictTokens
		counts.ContextCharacters += run.Observation.Receipt.Usage.ContextCharacters
		counts.IrrelevantLocators += run.Score.IrrelevantLocators
		counts.UnauthorizedLocators += run.Score.UnauthorizedLocators
		counts.WrongFactReads += run.Score.WrongFactReads
		counts.PermissionDenials += run.Observation.PermissionDenials
		counts.Truncations += run.Observation.Truncations
		_, codes := scoreFromStoredRun(run)
		failures = append(failures, failuresFor(run.RunID, codes)...)
	}
	report.Counts = counts
	report.Metrics = Metrics{
		LevelTop1Accuracy:  ratio(counts.LevelTop1Correct, counts.LevelsExpected),
		ExactPathSuccess:   ratio(counts.ExactPathSuccess, counts.Runs),
		RowIDSuccess:       ratio(counts.RowIDSuccess, counts.Runs),
		PredictorRecallAtK: ratio(counts.PredictorHits, counts.PredictorCases),
		PrefetchHitRate:    ratio(counts.PrefetchHits, counts.PrefetchCases),
		MeanFallbackCalls:  ratio(counts.FallbackCalls, counts.Runs),
		MeanModelCalls:     ratio(counts.ModelCalls, counts.Runs),
		MeanInputTokens:    ratio(counts.InputTokens, counts.Runs),
		MeanOutputTokens:   ratio(counts.OutputTokens, counts.Runs),
	}
	report.Failures = sortedFailureCopy(failures)
}

func scoreFromStoredRun(run RunReport) (RunScore, []string) {
	failures := []string{}
	if !run.Score.Completed {
		failures = append(failures, "host_not_completed")
	}
	if !run.Score.ExactPathSuccess {
		failures = append(failures, "exact_path_failed")
	}
	if !run.Score.RowIDSuccess {
		failures = append(failures, "rowid_failed")
	}
	if run.Score.UnauthorizedLocators > 0 {
		failures = append(failures, "unauthorized_locator")
	}
	if run.Score.WrongFactReads > 0 {
		failures = append(failures, "wrong_fact_read")
	}
	if run.Observation.PermissionDenials > 0 {
		failures = append(failures, "permission_denied")
	}
	if run.Observation.Truncations > 0 {
		failures = append(failures, "truncated")
	}
	return run.Score, failures
}

func reportDigest(report Report) (string, error) {
	report.Hash = ""
	encoded, err := json.Marshal(report)
	if err != nil {
		return "", runnerError(result.CodeInternal, "encode report identity: %v", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func ratio(numerator, denominator uint64) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func boolCount(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}

func equalArms(left, right []Arm) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
