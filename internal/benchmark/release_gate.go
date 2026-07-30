package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"

	"github.com/HW-Yue/Memora/internal/result"
)

const ReleaseBenchmarkVersion = "memora.ai-native-release-benchmark/v1"

type ReleaseThresholds struct {
	MinWritePrecision    float64 `json:"min_write_precision"`
	MinRecallAt5         float64 `json:"min_recall_at_5"`
	MinTakeoverSuccess   float64 `json:"min_takeover_success"`
	MaxContextCharacters float64 `json:"max_context_characters"`
	MaxVectorRecallGap   float64 `json:"max_vector_recall_gap"`
}

func DefaultReleaseThresholds() ReleaseThresholds {
	return ReleaseThresholds{
		MinWritePrecision: 0.95, MinRecallAt5: 0.90, MinTakeoverSuccess: 1,
		MaxContextCharacters: 2400, MaxVectorRecallGap: 0.05,
	}
}

type ReleaseGateResult struct {
	Status   string   `json:"status"`
	Failures []string `json:"failures"`
}

type ReleaseBenchmark struct {
	Version      string            `json:"version"`
	SuiteID      string            `json:"suite_id"`
	SuiteDigest  string            `json:"suite_digest"`
	CorpusID     string            `json:"corpus_id"`
	CorpusDigest string            `json:"corpus_digest"`
	Thresholds   ReleaseThresholds `json:"thresholds"`
	Model        ModelSnapshot     `json:"model_observations"`
	Reports      []Report          `json:"reports"`
	Gate         ReleaseGateResult `json:"gate"`
	Hash         string            `json:"hash"`
}

func RunReleaseBenchmark(
	ctx context.Context,
	suite Suite,
	corpus ReleaseCorpus,
	modeler SemanticModeler,
) (ReleaseBenchmark, error) {
	if err := suite.Validate(); err != nil {
		return ReleaseBenchmark{}, err
	}
	if err := corpus.Validate(); err != nil {
		return ReleaseBenchmark{}, err
	}
	if modeler == nil {
		return ReleaseBenchmark{}, benchmarkError(result.CodeInternal, "semantic modeler is not configured")
	}
	snapshot, err := modeler.Model(ctx, corpus)
	if err != nil {
		return ReleaseBenchmark{}, err
	}
	if err := validateModelSnapshot(corpus, snapshot.Records, snapshot.Queries); err != nil {
		return ReleaseBenchmark{}, err
	}
	suiteDigest, err := canonicalDigest(suite)
	if err != nil {
		return ReleaseBenchmark{}, err
	}
	corpusDigest, err := canonicalDigest(corpus)
	if err != nil {
		return ReleaseBenchmark{}, err
	}
	if snapshot.Evidence.Mode != "live-model" || snapshot.Evidence.OutputDigest == "" ||
		snapshot.Evidence.CorpusDigest != corpusDigest {
		return ReleaseBenchmark{}, benchmarkError(result.CodeValidation, "semantic model evidence is not a live run for this corpus")
	}
	reports := make([]Report, 0, len(BaselineNames()))
	implementations := map[string]string{
		"no-memory":       "empty-state/v1",
		"markdown-search": "markdown-lexical-bigram/v1",
		"sqlite-fts":      "sqlite-fts5-segmented/v1",
		"vector":          "local-character-trigram-cosine/v1",
		"memora":          "memora-route-agent-terms-mechanical-fusion/v1",
	}
	for _, name := range BaselineNames() {
		execution, err := executeReleaseAdapter(name, suite, corpus, snapshot)
		if err != nil {
			return ReleaseBenchmark{}, err
		}
		evidence := Evidence{
			Mode: "local-observed", OutputDigest: execution.digest, ExecutionDigest: execution.digest,
			CorpusDigest: corpusDigest, Implementation: implementations[name],
		}
		if name == "memora" {
			evidence = snapshot.Evidence
			evidence.ExecutionDigest = execution.digest
			evidence.Implementation = implementations[name]
		}
		report, err := NewRunner(&observedAdapter{name: name, outcomes: execution.outcomes, evidence: evidence}).Run(ctx, suite)
		if err != nil {
			return ReleaseBenchmark{}, err
		}
		reports = append(reports, report)
	}
	thresholds := DefaultReleaseThresholds()
	release := ReleaseBenchmark{
		Version: ReleaseBenchmarkVersion, SuiteID: suite.ID, SuiteDigest: suiteDigest,
		CorpusID: corpus.ID, CorpusDigest: corpusDigest, Thresholds: thresholds,
		Model: snapshot, Reports: reports,
	}
	release.Gate = evaluateReleaseGate(release)
	release.Hash, err = releaseBenchmarkDigest(release)
	if err != nil {
		return ReleaseBenchmark{}, err
	}
	return release, nil
}

func LoadReleaseBenchmark(path string) (ReleaseBenchmark, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return ReleaseBenchmark{}, benchmarkError(result.CodeInternal, "read release benchmark: %v", err)
	}
	var release ReleaseBenchmark
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&release); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ReleaseBenchmark{}, benchmarkError(result.CodeValidation, "decode release benchmark: %v", err)
	}
	if err := release.Validate(); err != nil {
		return ReleaseBenchmark{}, err
	}
	return release, nil
}

func (release ReleaseBenchmark) Validate() error {
	if release.Version != ReleaseBenchmarkVersion || release.SuiteID == "" || release.SuiteDigest == "" ||
		release.CorpusID == "" || release.CorpusDigest == "" || len(release.Reports) != len(BaselineNames()) {
		return benchmarkError(result.CodeValidation, "release benchmark identity and all baseline reports are required")
	}
	if err := validateModelSnapshotFromRelease(release); err != nil {
		return err
	}
	names := map[string]bool{}
	for _, report := range release.Reports {
		if names[report.Adapter] || report.SuiteID != release.SuiteID || report.SuiteDigest != release.SuiteDigest ||
			report.Evidence == nil || report.Evidence.CorpusDigest != release.CorpusDigest ||
			report.Evidence.OutputDigest == "" || report.Evidence.ExecutionDigest == "" {
			return benchmarkError(result.CodeValidation, "release report %q has invalid identity or evidence", report.Adapter)
		}
		names[report.Adapter] = true
		expectedHash, err := reportDigest(report)
		if err != nil || report.Hash != expectedHash {
			return benchmarkError(result.CodeValidation, "release report %q hash mismatch", report.Adapter)
		}
	}
	for _, name := range BaselineNames() {
		if !names[name] {
			return benchmarkError(result.CodeValidation, "release benchmark omits adapter %q", name)
		}
	}
	expectedGate := evaluateReleaseGate(release)
	if !reflect.DeepEqual(release.Gate, expectedGate) {
		return benchmarkError(result.CodeValidation, "release gate result does not match report evidence")
	}
	expectedHash, err := releaseBenchmarkDigest(release)
	if err != nil || release.Hash != expectedHash {
		return benchmarkError(result.CodeValidation, "release benchmark hash mismatch")
	}
	return nil
}

func validateModelSnapshotFromRelease(release ReleaseBenchmark) error {
	if release.Model.Evidence.Mode != "live-model" ||
		release.Model.Evidence.CorpusDigest != release.CorpusDigest ||
		release.Model.Evidence.OutputDigest == "" ||
		release.Model.Evidence.Protocol == "" || release.Model.Evidence.Model == "" {
		return benchmarkError(result.CodeValidation, "release benchmark lacks live model evidence")
	}
	return nil
}

func evaluateReleaseGate(release ReleaseBenchmark) ReleaseGateResult {
	failures := []string{}
	reports := map[string]Report{}
	for _, report := range release.Reports {
		reports[report.Adapter] = report
		if !report.AllHostsEqual {
			failures = append(failures, fmt.Sprintf("%s host results differ", report.Adapter))
		}
	}
	memora, memoraOK := reports["memora"]
	markdown, markdownOK := reports["markdown-search"]
	vector, vectorOK := reports["vector"]
	if !memoraOK || !markdownOK || !vectorOK {
		failures = append(failures, "required comparison reports are missing")
	} else {
		metrics, thresholds := memora.Aggregate, release.Thresholds
		if metrics.WritePrecision < thresholds.MinWritePrecision {
			failures = append(failures, metricFailure("memora write precision", metrics.WritePrecision, ">=", thresholds.MinWritePrecision))
		}
		if metrics.RecallAt5 < thresholds.MinRecallAt5 {
			failures = append(failures, metricFailure("memora Recall@5", metrics.RecallAt5, ">=", thresholds.MinRecallAt5))
		}
		if metrics.TakeoverSuccessRate < thresholds.MinTakeoverSuccess {
			failures = append(failures, metricFailure("memora takeover success", metrics.TakeoverSuccessRate, ">=", thresholds.MinTakeoverSuccess))
		}
		if metrics.MeanContextCharacters > thresholds.MaxContextCharacters {
			failures = append(failures, metricFailure("memora mean context characters", metrics.MeanContextCharacters, "<=", thresholds.MaxContextCharacters))
		}
		if metrics.WritePrecision <= markdown.Aggregate.WritePrecision {
			failures = append(failures, "memora write precision does not exceed markdown-search")
		}
		if metrics.RecallAt5 <= markdown.Aggregate.RecallAt5 {
			failures = append(failures, "memora Recall@5 does not exceed markdown-search")
		}
		if metrics.RecallAt5+thresholds.MaxVectorRecallGap < vector.Aggregate.RecallAt5 {
			failures = append(failures, "memora Recall@5 trails vector beyond the allowed gap")
		}
	}
	sort.Strings(failures)
	status := "passed"
	if len(failures) > 0 {
		status = "failed"
	}
	return ReleaseGateResult{Status: status, Failures: failures}
}

func metricFailure(name string, actual float64, operator string, threshold float64) string {
	return fmt.Sprintf("%s %.6f must be %s %.6f", name, actual, operator, threshold)
}

func releaseBenchmarkDigest(release ReleaseBenchmark) (string, error) {
	release.Hash = ""
	return canonicalDigest(release)
}
