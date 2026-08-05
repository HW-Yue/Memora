package answerbenchmark

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/answercorpus"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

var (
	ErrInvalidRunner = errors.New("answer benchmark Runner is invalid")
	ErrRunBenchmark  = errors.New("answer benchmark run failed")
)

const (
	ArmAtlasOnly            = string(agent.BootstrapProfileAtlasOnly)
	ArmAtlasLexical         = string(agent.BootstrapProfileAtlasLexical)
	ArmAtlasLexicalPrefetch = string(agent.BootstrapProfileAtlasLexicalPrefetch)
)

type RunnerDependencies struct {
	MSQL     agent.MSQLExecutor
	Provider agent.Provider
	Clock    agent.QueryClock
}

type RunConfig struct {
	RunID          string
	ProviderID     string
	Model          string
	ArmID          string
	PromptID       string
	CodeRevision   string
	CheckpointPath string
}

type Runner struct {
	msql     agent.MSQLExecutor
	provider agent.Provider
	clock    agent.QueryClock
}

func NewRunner(dependencies RunnerDependencies) (*Runner, error) {
	if dependencies.MSQL == nil || dependencies.Provider == nil || dependencies.Clock == nil {
		return nil, ErrInvalidRunner
	}
	return &Runner{msql: dependencies.MSQL, provider: dependencies.Provider, clock: dependencies.Clock}, nil
}

func (runner *Runner) Run(
	ctx context.Context,
	manifest answercorpus.Manifest,
	config RunConfig,
) (PublicScorecard, PrivateDiagnostics, error) {
	if runner == nil || runner.msql == nil || runner.provider == nil || runner.clock == nil || ctx == nil ||
		!validRunConfig(config) {
		return PublicScorecard{}, PrivateDiagnostics{}, ErrInvalidRunner
	}
	if err := manifest.Validate(); err != nil {
		return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("%w: public manifest is invalid", ErrRunBenchmark)
	}
	if err := ctx.Err(); err != nil {
		return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("%w: %w", ErrRunBenchmark, err)
	}
	materializer, err := NewMaterializer(runner.msql)
	if err != nil {
		return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("%w: %v", ErrRunBenchmark, err)
	}
	receipt, err := materializer.Materialize(ctx, manifest)
	if err != nil {
		return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("%w: %v", ErrRunBenchmark, err)
	}
	tasks, err := manifest.BlindTasks()
	if err != nil {
		return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("%w: blind tasks are invalid", ErrRunBenchmark)
	}
	checkpointCases := make(map[string]CheckpointCase)
	if config.CheckpointPath != "" {
		checkpoint, checkpointErr := LoadCheckpoint(config.CheckpointPath)
		if checkpointErr == nil {
			if checkpoint.Identity != CheckpointIdentity(manifest, config) {
				return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("%w: checkpoint identity mismatch", ErrRunBenchmark)
			}
			taskIDs := make(map[string]struct{}, len(tasks))
			for _, task := range tasks {
				taskIDs[task.CaseID] = struct{}{}
			}
			for _, value := range checkpoint.Cases {
				if _, exists := taskIDs[value.Public.CaseID]; !exists {
					return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("%w: checkpoint contains an unknown case", ErrRunBenchmark)
				}
				checkpointCases[value.Public.CaseID] = value
			}
		} else if !errors.Is(checkpointErr, os.ErrNotExist) {
			return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("%w: load checkpoint: %v", ErrRunBenchmark, checkpointErr)
		}
	}
	queryAgent, err := agent.NewQueryAgent(agent.QueryAgentDependencies{
		MSQL: runner.msql, Provider: runner.provider, Clock: runner.clock,
	})
	if err != nil {
		return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("%w: Query Agent dependencies: %v", ErrRunBenchmark, err)
	}
	public := PublicScorecard{
		Version: PublicScorecardVersion, RunID: config.RunID, CorpusID: manifest.CorpusID,
		CorpusRevision: manifest.Revision, SnapshotID: manifest.Fixture.SnapshotID,
		SnapshotSHA256: manifest.Fixture.SnapshotSHA256, ProviderID: config.ProviderID, Model: config.Model,
		ArmID: config.ArmID, PromptID: config.PromptID, CodeRevision: config.CodeRevision,
		QualityStatus: QualityNotScored, Cases: make([]PublicCase, 0, len(tasks)),
	}
	private := PrivateDiagnostics{
		Version: PrivateDiagnosticsVersion, RunID: config.RunID, Materialization: receipt,
		Cases: make([]PrivateCase, 0, len(tasks)),
	}
	persistCheckpoint := func() error {
		if config.CheckpointPath == "" {
			return nil
		}
		ordered := make([]CheckpointCase, 0, len(checkpointCases))
		for _, task := range tasks {
			if value, exists := checkpointCases[task.CaseID]; exists {
				ordered = append(ordered, value)
			}
		}
		checkpoint := Checkpoint{Version: CheckpointVersion, Identity: CheckpointIdentity(manifest, config), Cases: ordered}
		if err := checkpoint.Seal(); err != nil {
			return err
		}
		return SaveCheckpoint(config.CheckpointPath, checkpoint)
	}
	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("%w: %w", ErrRunBenchmark, err)
		}
		if checkpoint, exists := checkpointCases[task.CaseID]; exists && checkpoint.Public.Status == "succeeded" {
			public.Cases = append(public.Cases, checkpoint.Public)
			private.Cases = append(private.Cases, checkpoint.Private)
			continue
		}
		started := runner.clock.Now().UTC()
		result, queryErr := queryAgent.Query(ctx, queryRequest(config, task))
		finished := runner.clock.Now().UTC()
		if queryErr != nil && (errors.Is(queryErr, context.Canceled) || errors.Is(queryErr, context.DeadlineExceeded) || ctx.Err() != nil) {
			if ctx.Err() != nil {
				queryErr = ctx.Err()
			}
			return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("%w: %w", ErrRunBenchmark, queryErr)
		}
		public.Cases = append(public.Cases, publicCase(task, result, queryErr, elapsedMicros(started, finished)))
		private.Cases = append(private.Cases, PrivateCase{
			Task: task, Error: privateError(queryErr), Evidence: nonNilEvidence(result.Evidence), Trace: result.Trace,
		})
		checkpointCases[task.CaseID] = CheckpointCase{Public: public.Cases[len(public.Cases)-1], Private: private.Cases[len(private.Cases)-1]}
		if err := persistCheckpoint(); err != nil {
			return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("%w: save checkpoint: %v", ErrRunBenchmark, err)
		}
	}
	if err := public.Seal(); err != nil {
		return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("%w: public scorecard: %v", ErrRunBenchmark, err)
	}
	private.PublicScorecardSHA256 = public.Hash
	if err := private.Seal(); err != nil {
		return PublicScorecard{}, PrivateDiagnostics{}, fmt.Errorf("%w: private diagnostics: %v", ErrRunBenchmark, err)
	}
	return public, private, nil
}

func validRunConfig(config RunConfig) bool {
	_, validArm := bootstrapProfileForArm(config.ArmID)
	return validReportText(config.RunID, 200) && validReportText(config.ProviderID, 200) &&
		validReportText(config.Model, 200) && validArm &&
		validReportText(config.PromptID, 200) && validReportText(config.CodeRevision, 200)
}

func bootstrapProfileForArm(armID string) (agent.BootstrapProfile, bool) {
	switch armID {
	case ArmAtlasOnly:
		return agent.BootstrapProfileAtlasOnly, true
	case ArmAtlasLexical:
		return agent.BootstrapProfileAtlasLexical, true
	case ArmAtlasLexicalPrefetch:
		return agent.BootstrapProfileAtlasLexicalPrefetch, true
	default:
		return "", false
	}
}

func queryRequest(config RunConfig, task answercorpus.BlindTask) agent.QueryRequest {
	profile, _ := bootstrapProfileForArm(config.ArmID)
	bootstrap := agent.DefaultBootstrapBudget()
	bootstrap.FrameUTF8Bytes = task.Budget.BootstrapFrameUTF8Bytes
	budget := agent.DefaultQueryBudget()
	budget.MaxProviderCalls = task.Budget.MaxProviderCalls
	budget.MaxToolCalls = task.Budget.MaxToolCalls
	budget.MaxOutputTokens = task.Budget.MaxOutputTokens
	budget.ToolResultUTF8Bytes = task.Budget.ToolResultUTF8Bytes
	return agent.QueryRequest{
		Identity: agent.TraceIdentity{RunID: config.RunID, SessionID: config.RunID + ":" + task.CaseID},
		Question: task.Question, Model: config.Model,
		Authorization: protocolmsql.Authorization{
			Version: protocolmsql.AuthorizationVersion, Actor: "benchmark:query-agent",
			AuthorizedDatabases: append([]string(nil), task.AuthorizedDatabases...), DefaultLevel: protocolmsql.LevelRead,
		},
		BootstrapBudget: bootstrap, BootstrapProfile: profile, Budget: budget,
	}
}

func publicCase(task answercorpus.BlindTask, result agent.QueryResult, queryErr error, duration uint64) PublicCase {
	summary := result.Trace.Summary
	value := PublicCase{
		CaseID: task.CaseID, Question: task.Question, Status: "succeeded", Answer: result.Answer,
		QualityStatus: QualityNotScored, DurationMicros: duration,
		ProviderCalls: summary.KindCounts[agent.TraceKindProvider], MSQLCalls: summary.KindCounts[agent.TraceKindMSQL],
		ToolCalls: summary.KindCounts[agent.TraceKindTool], InputTokens: summary.InputTokens,
		CachedTokens: summary.CachedInputTokens, OutputTokens: summary.OutputTokens, TotalTokens: summary.TotalTokens,
	}
	if queryErr != nil {
		value.Status, value.Answer, value.ErrorCode = "failed", "", publicErrorCode(queryErr)
	}
	return value
}

func publicErrorCode(err error) string {
	switch {
	case errors.Is(err, agent.ErrQueryBudget):
		return "query_budget"
	case errors.Is(err, agent.ErrQueryMissingSelectEvidence):
		return "missing_select_evidence"
	case errors.Is(err, agent.ErrInvalidQueryToolCall), errors.Is(err, agent.ErrQueryTransactionControl):
		return "invalid_tool_call"
	case errors.Is(err, agent.ErrQueryIncompleteAnswer):
		return "incomplete_answer"
	default:
		return "query_failed"
	}
}

func privateError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func nonNilEvidence(value []agent.SelectEvidence) []agent.SelectEvidence {
	if value == nil {
		return []agent.SelectEvidence{}
	}
	return value
}

func elapsedMicros(started, finished time.Time) uint64 {
	if started.IsZero() || finished.Before(started) {
		return 0
	}
	return uint64(finished.Sub(started) / time.Microsecond)
}
