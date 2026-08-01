package routebenchmarkrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/hostcontract"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routebenchmark"
)

type Runner struct{ options Options }

func New(options Options) (*Runner, error) {
	if err := options.Contract.Validate(); err != nil {
		return nil, err
	}
	if !validDigest(options.ContractDigest) || !validDigest(options.SkillDigest) ||
		!validDigest(options.ProtocolDigest) {
		return nil, runnerError(result.CodeValidation, "contract, Skill, and protocol digests are required")
	}
	if len(options.Profiles) < 3 {
		return nil, runnerError(result.CodeValidation, "host matrix requires at least three profiles")
	}
	seen := map[string]bool{}
	for index, profile := range options.Profiles {
		if profile.Driver == nil {
			return nil, runnerError(result.CodeValidation, "profile %d driver is not configured", index)
		}
		key := profileKey(profile.Host)
		if seen[key] {
			return nil, runnerError(result.CodeValidation, "host profile %d is duplicated", index)
		}
		seen[key] = true
	}
	options.Profiles = append([]Profile(nil), options.Profiles...)
	return &Runner{options: options}, nil
}

func (runner *Runner) Run(ctx context.Context, corpus routebenchmark.Corpus) (Report, error) {
	if runner == nil {
		return Report{}, runnerError(result.CodeInternal, "runner is not configured")
	}
	if err := corpus.Validate(); err != nil {
		return Report{}, err
	}
	profiles := make([]hostcontract.HostProfile, 0, len(runner.options.Profiles))
	for _, profile := range runner.options.Profiles {
		profiles = append(profiles, profile.Host)
	}
	report := Report{
		Version: ReportVersion, Runner: RunnerVersion, CorpusID: corpus.ID, CorpusDigest: corpus.Hash,
		ContractDigest: runner.options.ContractDigest, SkillDigest: runner.options.SkillDigest,
		ProtocolDigest: runner.options.ProtocolDigest, Arms: Arms(), Profiles: profiles,
		Runs: []RunReport{}, Failures: []Failure{},
	}
	for _, scenario := range corpus.Scenarios {
		if err := ctx.Err(); err != nil {
			return Report{}, runnerError(result.CodeCancelled, "benchmark cancelled: %v", err)
		}
		task, err := corpus.HostTask(scenario.ID)
		if err != nil {
			return Report{}, err
		}
		taskDigest, err := runner.options.Contract.TaskDigest(task)
		if err != nil {
			return Report{}, err
		}
		matrix := make([]hostcontract.Invocation, 0, len(runner.options.Profiles))
		for _, profile := range runner.options.Profiles {
			matrix = append(matrix, hostcontract.Invocation{
				Version: hostcontract.InvocationVersion, TaskDigest: taskDigest,
				SkillDigest: runner.options.SkillDigest, ProtocolDigest: runner.options.ProtocolDigest,
				Host: profile.Host,
			})
		}
		if err := runner.options.Contract.ValidateMatrix(matrix); err != nil {
			return Report{}, err
		}
		for _, arm := range canonicalArms {
			for _, profile := range runner.options.Profiles {
				request, err := runner.request(corpus, scenario, task, taskDigest, arm, profile.Host)
				if err != nil {
					return Report{}, err
				}
				observation, err := profile.Driver.Run(ctx, request)
				if err != nil {
					return Report{}, runnerError(result.CodeInternal, "driver failed for run %s", request.RunID)
				}
				if err := runner.validateObservation(task, request, observation); err != nil {
					return Report{}, err
				}
				score, failures := scoreObservation(scenario, arm, observation)
				report.Runs = append(report.Runs, RunReport{
					RunID: request.RunID, ScenarioID: scenario.ID, TaskDigest: taskDigest,
					Snapshot: scenario.Snapshot, Topic: scenario.Topic, Difficulty: scenario.Difficulty,
					Language: scenario.Language, Fanout: scenario.Fanout, Depth: scenario.Depth,
					Arm: arm, Host: profile.Host, Observation: observation, Score: score,
				})
				report.Failures = append(report.Failures, failuresFor(request.RunID, failures)...)
			}
		}
	}
	if err := report.Seal(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func (runner *Runner) request(
	corpus routebenchmark.Corpus,
	scenario routebenchmark.Scenario,
	task hostcontract.Task,
	taskDigest string,
	arm Arm,
	host hostcontract.HostProfile,
) (Request, error) {
	identity := struct {
		Version        string                   `json:"version"`
		CorpusDigest   string                   `json:"corpus_digest"`
		ScenarioID     string                   `json:"scenario_id"`
		Snapshot       string                   `json:"snapshot"`
		Arm            Arm                      `json:"arm"`
		TaskDigest     string                   `json:"task_digest"`
		SkillDigest    string                   `json:"skill_digest"`
		ProtocolDigest string                   `json:"protocol_digest"`
		Host           hostcontract.HostProfile `json:"host"`
	}{RequestVersion, corpus.Hash, scenario.ID, scenario.Snapshot, arm, taskDigest,
		runner.options.SkillDigest, runner.options.ProtocolDigest, host}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return Request{}, runnerError(result.CodeInternal, "encode run identity: %v", err)
	}
	digest := sha256.Sum256(encoded)
	return Request{
		Version: RequestVersion, RunID: hex.EncodeToString(digest[:]), Arm: arm,
		TaskDigest: taskDigest, SkillDigest: runner.options.SkillDigest,
		ProtocolDigest: runner.options.ProtocolDigest, CorpusDigest: corpus.Hash, Host: host,
		Task: cloneTask(task), Fixture: fixtureFor(scenario),
	}, nil
}

func (runner *Runner) validateObservation(
	task hostcontract.Task,
	request Request,
	observation Observation,
) error {
	if observation.Version != ObservationVersion || observation.RunID != request.RunID ||
		observation.TaskDigest != request.TaskDigest || observation.SkillDigest != request.SkillDigest ||
		observation.ProtocolDigest != request.ProtocolDigest || observation.CorpusDigest != request.CorpusDigest ||
		observation.Snapshot != request.Fixture.Snapshot || observation.Arm != request.Arm {
		return runnerError(result.CodeValidation, "observation identity differs for run %s", request.RunID)
	}
	if !reflect.DeepEqual(observation.Receipt.Host, request.Host) ||
		observation.Receipt.SkillDigest != request.SkillDigest ||
		observation.Receipt.ProtocolDigest != request.ProtocolDigest {
		return runnerError(result.CodeValidation, "receipt host or digest differs for run %s", request.RunID)
	}
	if err := runner.options.Contract.ValidateReceipt(task, observation.Receipt); err != nil {
		return runnerError(result.CodeValidation, "receipt rejected for run %s: %v", request.RunID, err)
	}
	if observation.Timing.EndToEndMillis > observation.Receipt.Usage.ElapsedMillis ||
		observation.MispredictTokens > observation.Receipt.Usage.InputTokens+observation.Receipt.Usage.OutputTokens {
		return runnerError(result.CodeValidation, "observation counters contradict receipt for run %s", request.RunID)
	}
	return nil
}

func scoreObservation(scenario routebenchmark.Scenario, arm Arm, observation Observation) (RunScore, []string) {
	routes, roots, rows, expectedPath := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, node := range scenario.Nodes {
		routes[node.RouteID] = true
		for _, rowID := range node.RowIDs {
			rows[rowID] = true
		}
	}
	for _, routeID := range scenario.Table.RootRouteIDs {
		roots[routeID] = true
	}
	for _, routeID := range scenario.Expected.Path {
		expectedPath[routeID] = true
	}
	score := RunScore{Completed: observation.Receipt.Status == "completed"}
	if scenario.Expected.Match {
		score.LevelsExpected = uint64(len(scenario.Expected.Path))
		for index, routeID := range scenario.Expected.Path {
			if index < len(observation.LevelChoices) && observation.LevelChoices[index] == routeID {
				score.LevelTop1Correct++
			}
		}
		score.ExactPathSuccess = score.Completed && equalStrings(observation.LevelChoices, scenario.Expected.Path)
	} else {
		score.ExactPathSuccess = score.Completed && len(observation.LevelChoices) == 0
	}
	wrongFacts := uint64(0)
	for _, rowID := range observation.FactReadRowIDs {
		if !scenario.Expected.Match || rowID != scenario.Expected.RowID {
			wrongFacts++
		}
	}
	if scenario.Expected.Match {
		score.RowIDSuccess = score.Completed && observation.SelectedRowID == scenario.Expected.RowID &&
			containsString(observation.FactReadRowIDs, scenario.Expected.RowID) && wrongFacts == 0
	} else {
		score.RowIDSuccess = score.Completed && observation.SelectedRowID == "" && len(observation.FactReadRowIDs) == 0
	}
	score.WrongFactReads = wrongFacts
	if scenario.Expected.Match && arm != ArmRouter {
		score.PredictorEligible = true
		for _, routeID := range observation.PredictorRouteIDs {
			if expectedPath[routeID] {
				score.PredictorHit = true
			}
		}
	}
	if scenario.Expected.Match && arm == ArmHybridPrefetch {
		score.PrefetchEligible = true
		score.PrefetchHit = containsString(observation.PrefetchedRootRouteIDs, scenario.Expected.Path[0])
	}
	for _, routeID := range observation.LevelChoices {
		if !routes[routeID] {
			score.UnauthorizedLocators++
		} else if !expectedPath[routeID] {
			score.IrrelevantLocators++
		}
	}
	for _, routeID := range observation.PredictorRouteIDs {
		if !routes[routeID] {
			score.UnauthorizedLocators++
		} else if !expectedPath[routeID] {
			score.IrrelevantLocators++
		}
	}
	for _, routeID := range observation.PrefetchedRootRouteIDs {
		if !roots[routeID] {
			score.UnauthorizedLocators++
		} else if scenario.Expected.Match && routeID != scenario.Expected.Path[0] {
			score.IrrelevantLocators++
		}
	}
	if observation.SelectedRowID != "" && !rows[observation.SelectedRowID] {
		score.UnauthorizedLocators++
	}
	for _, rowID := range observation.FactReadRowIDs {
		if !rows[rowID] {
			score.UnauthorizedLocators++
		}
	}
	failures := []string{}
	if !score.Completed {
		failures = append(failures, "host_not_completed")
	}
	if !score.ExactPathSuccess {
		failures = append(failures, "exact_path_failed")
	}
	if !score.RowIDSuccess {
		failures = append(failures, "rowid_failed")
	}
	if score.UnauthorizedLocators > 0 {
		failures = append(failures, "unauthorized_locator")
	}
	if score.WrongFactReads > 0 {
		failures = append(failures, "wrong_fact_read")
	}
	if observation.PermissionDenials > 0 {
		failures = append(failures, "permission_denied")
	}
	if observation.Truncations > 0 {
		failures = append(failures, "truncated")
	}
	return score, failures
}

func failuresFor(runID string, codes []string) []Failure {
	result := make([]Failure, 0, len(codes))
	for _, code := range codes {
		result = append(result, Failure{RunID: runID, Code: code})
	}
	return result
}

func fixtureFor(scenario routebenchmark.Scenario) Fixture {
	nodes := make([]routebenchmark.Node, len(scenario.Nodes))
	for index, node := range scenario.Nodes {
		node.Aliases = append([]string(nil), node.Aliases...)
		node.ChildRouteIDs = append([]string(nil), node.ChildRouteIDs...)
		node.RowIDs = append([]string(nil), node.RowIDs...)
		nodes[index] = node
	}
	table := scenario.Table
	table.RootRouteIDs = append([]string(nil), table.RootRouteIDs...)
	return Fixture{Snapshot: scenario.Snapshot, Database: scenario.Database, Table: table, Nodes: nodes}
}

func cloneTask(task hostcontract.Task) hostcontract.Task {
	task.AuthorizedDatabases = append([]string(nil), task.AuthorizedDatabases...)
	return task
}

func profileKey(host hostcontract.HostProfile) string {
	return strings.Join([]string{host.Surface, host.Provider, host.Protocol, host.Model, host.EndpointHost}, "\x00")
}

func validDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func equalStrings(left, right []string) bool {
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

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func sortedFailureCopy(values []Failure) []Failure {
	result := append(make([]Failure, 0, len(values)), values...)
	sort.Slice(result, func(left, right int) bool {
		if result[left].RunID != result[right].RunID {
			return result[left].RunID < result[right].RunID
		}
		return result[left].Code < result[right].Code
	})
	return result
}
