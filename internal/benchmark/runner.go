package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/HW-Yue/Memora/internal/result"
)

type Runner struct{ adapter Adapter }

func NewRunner(adapter Adapter) *Runner { return &Runner{adapter: adapter} }

func (runner *Runner) Run(ctx context.Context, suite Suite) (Report, error) {
	if err := suite.Validate(); err != nil {
		return Report{}, err
	}
	if runner == nil || runner.adapter == nil || strings.TrimSpace(runner.adapter.Name()) == "" {
		return Report{}, benchmarkError(result.CodeInternal, "benchmark adapter is not configured")
	}
	suiteDigest, err := canonicalDigest(suite)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		Version: ReportVersion, SuiteID: suite.ID, SuiteDigest: suiteDigest,
		Adapter: runner.adapter.Name(), Dimensions: 8, Scenarios: []ScenarioReport{}, AllHostsEqual: true,
	}
	if adapter, ok := runner.adapter.(evidenceAdapter); ok {
		evidence := adapter.Evidence()
		report.Evidence = &evidence
	}
	var aggregate Counts
	for _, scenario := range suite.Scenarios {
		if err := ctx.Err(); err != nil {
			return Report{}, benchmarkError(result.CodeCancelled, "benchmark cancelled: %v", err)
		}
		outcome, err := runner.adapter.Run(ctx, scenario)
		if err != nil {
			return Report{}, err
		}
		if err := outcome.Counts.Validate(); err != nil {
			return Report{}, benchmarkError(result.CodeValidation, "scenario %q outcome: %v", scenario.ID, err)
		}
		report.Scenarios = append(report.Scenarios, ScenarioReport{
			ScenarioID: scenario.ID, Kind: scenario.Kind, HostResultsEquivalent: outcome.HostResultsEquivalent,
			Counts: outcome.Counts, Metrics: score(outcome.Counts),
		})
		report.AllHostsEqual = report.AllHostsEqual && outcome.HostResultsEquivalent
		aggregate.add(outcome.Counts)
	}
	report.Aggregate = score(aggregate)
	report.Hash, err = reportDigest(report)
	if err != nil {
		return Report{}, err
	}
	return report, nil
}

func (counts Counts) Validate() error {
	pairs := [][2]uint64{
		{counts.WritesCorrect, counts.WritesAttempted}, {counts.WritesCorrect, counts.WritesExpected},
		{counts.SourceUnitsRead, counts.SourceUnitsRequired}, {counts.ClaimsCorrect, counts.ClaimsChecked},
		{counts.AnchorsTraceable, counts.AnchorsChecked}, {counts.SchemaDuplicates, counts.SchemaObjects},
		{counts.RetrievalHitsAt5, counts.RetrievalQueries}, {counts.IrrelevantRows, counts.SelectedRows},
		{counts.UnintendedRows, counts.MutatedRows}, {counts.RevisionConflictsCaptured, counts.RevisionConflicts},
		{counts.TakeoversSucceeded, counts.Takeovers}, {counts.RecoveriesSucceeded, counts.Recoveries},
		{counts.IndexChecksPassed, counts.IndexChecks}, {counts.ExportHashesMatched, counts.ExportChecks},
	}
	for _, pair := range pairs {
		if pair[0] > pair[1] {
			return benchmarkError(result.CodeValidation, "successful count %d exceeds total %d", pair[0], pair[1])
		}
	}
	if counts.ReciprocalRankMilli > counts.RetrievalQueries*1000 || counts.NDCGMilli > counts.RetrievalQueries*1000 {
		return benchmarkError(result.CodeValidation, "ranking score exceeds its query total")
	}
	return nil
}

func score(counts Counts) Metrics {
	return Metrics{
		WritePrecision: ratio(counts.WritesCorrect, counts.WritesAttempted), WriteRecall: ratio(counts.WritesCorrect, counts.WritesExpected),
		AssimilationCoverage: ratio(counts.SourceUnitsRead, counts.SourceUnitsRequired), ClaimAccuracy: ratio(counts.ClaimsCorrect, counts.ClaimsChecked),
		AnchorTraceability: ratio(counts.AnchorsTraceable, counts.AnchorsChecked), SchemaDuplicateRate: ratio(counts.SchemaDuplicates, counts.SchemaObjects),
		RecallAt5: ratio(counts.RetrievalHitsAt5, counts.RetrievalQueries), MRR: milliRatio(counts.ReciprocalRankMilli, counts.RetrievalQueries),
		NDCG: milliRatio(counts.NDCGMilli, counts.RetrievalQueries), MeanContextCharacters: ratio(counts.ContextCharacters, counts.AnsweredTurns),
		MeanToolCalls: ratio(counts.ToolCalls, counts.AnsweredTurns), IrrelevantRowRate: ratio(counts.IrrelevantRows, counts.SelectedRows),
		UnintendedRowRate: ratio(counts.UnintendedRows, counts.MutatedRows), RevisionConflictCaptureRate: ratio(counts.RevisionConflictsCaptured, counts.RevisionConflicts),
		TakeoverSuccessRate: ratio(counts.TakeoversSucceeded, counts.Takeovers), CrashRecoveryRate: ratio(counts.RecoveriesSucceeded, counts.Recoveries),
		IndexConsistencyRate: ratio(counts.IndexChecksPassed, counts.IndexChecks), DeterministicExportRate: ratio(counts.ExportHashesMatched, counts.ExportChecks),
	}
}

func ratio(numerator, denominator uint64) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func milliRatio(total, count uint64) float64 {
	if count == 0 {
		return 0
	}
	return float64(total) / float64(count*1000)
}

func canonicalDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", benchmarkError(result.CodeInternal, "encode benchmark identity: %v", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func reportDigest(report Report) (string, error) {
	report.Hash = ""
	return canonicalDigest(report)
}

func (counts *Counts) add(other Counts) {
	left, right := reflect.ValueOf(counts).Elem(), reflect.ValueOf(other)
	for index := 0; index < left.NumField(); index++ {
		left.Field(index).SetUint(left.Field(index).Uint() + right.Field(index).Uint())
	}
}
