package answerbenchmark

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/answercorpus"
)

const (
	PublicScorecardVersion    = "memora.answer-scorecard/v1"
	PrivateDiagnosticsVersion = "memora.answer-diagnostics/v1"
	QualityNotScored          = "not_scored"
)

var ErrInvalidReport = errors.New("answer benchmark report is invalid")

type PublicCase struct {
	CaseID         string `json:"case_id"`
	Question       string `json:"question"`
	Status         string `json:"status"`
	ErrorCode      string `json:"error_code,omitempty"`
	Answer         string `json:"answer,omitempty"`
	QualityStatus  string `json:"quality_status"`
	DurationMicros uint64 `json:"duration_micros"`
	ProviderCalls  uint64 `json:"provider_calls"`
	MSQLCalls      uint64 `json:"msql_calls"`
	ToolCalls      uint64 `json:"tool_calls"`
	InputTokens    uint64 `json:"input_tokens"`
	CachedTokens   uint64 `json:"cached_input_tokens"`
	OutputTokens   uint64 `json:"output_tokens"`
	TotalTokens    uint64 `json:"total_tokens"`
}

type PublicScorecard struct {
	Version        string       `json:"version"`
	RunID          string       `json:"run_id"`
	CorpusID       string       `json:"corpus_id"`
	CorpusRevision uint64       `json:"corpus_revision"`
	SnapshotID     string       `json:"snapshot_id"`
	SnapshotSHA256 string       `json:"snapshot_sha256"`
	ProviderID     string       `json:"provider_id"`
	Model          string       `json:"model"`
	ArmID          string       `json:"arm_id"`
	PromptID       string       `json:"prompt_id"`
	CodeRevision   string       `json:"code_revision"`
	QualityStatus  string       `json:"quality_status"`
	Cases          []PublicCase `json:"cases"`
	Hash           string       `json:"hash"`
}

func (scorecard *PublicScorecard) Seal() error {
	if scorecard == nil || !validPublicScorecard(*scorecard, false) {
		return ErrInvalidReport
	}
	digest, err := publicScorecardDigest(*scorecard)
	if err != nil {
		return ErrInvalidReport
	}
	scorecard.Hash = digest
	return nil
}

func (scorecard PublicScorecard) Validate() error {
	if !validPublicScorecard(scorecard, true) {
		return ErrInvalidReport
	}
	digest, err := publicScorecardDigest(scorecard)
	if err != nil || scorecard.Hash != digest {
		return ErrInvalidReport
	}
	return nil
}

type PrivateCase struct {
	Task     answercorpus.BlindTask `json:"task"`
	Error    string                 `json:"error,omitempty"`
	Evidence []agent.SelectEvidence `json:"evidence"`
	Trace    agent.TraceEnvelope    `json:"trace"`
}

type PrivateDiagnostics struct {
	Version               string                 `json:"version"`
	RunID                 string                 `json:"run_id"`
	PublicScorecardSHA256 string                 `json:"public_scorecard_sha256"`
	Materialization       MaterializationReceipt `json:"materialization"`
	Cases                 []PrivateCase          `json:"cases"`
	Hash                  string                 `json:"hash"`
}

func (diagnostics *PrivateDiagnostics) Seal() error {
	if diagnostics == nil || !validPrivateDiagnostics(*diagnostics, false) {
		return ErrInvalidReport
	}
	digest, err := privateDiagnosticsDigest(*diagnostics)
	if err != nil {
		return ErrInvalidReport
	}
	diagnostics.Hash = digest
	return nil
}

func (diagnostics PrivateDiagnostics) Validate() error {
	if !validPrivateDiagnostics(diagnostics, true) {
		return ErrInvalidReport
	}
	digest, err := privateDiagnosticsDigest(diagnostics)
	if err != nil || diagnostics.Hash != digest {
		return ErrInvalidReport
	}
	return nil
}

func validPublicScorecard(scorecard PublicScorecard, sealed bool) bool {
	if scorecard.Version != PublicScorecardVersion || !validReportText(scorecard.RunID, 200) ||
		!validReportText(scorecard.CorpusID, 200) || scorecard.CorpusRevision == 0 ||
		!validReportText(scorecard.SnapshotID, 200) || !validDigest(scorecard.SnapshotSHA256) ||
		!validReportText(scorecard.ProviderID, 200) || !validReportText(scorecard.Model, 200) ||
		!validReportText(scorecard.ArmID, 200) || !validReportText(scorecard.PromptID, 200) ||
		!validReportText(scorecard.CodeRevision, 200) || scorecard.QualityStatus != QualityNotScored ||
		len(scorecard.Cases) == 0 || scorecard.Cases == nil || sealed != validDigest(scorecard.Hash) {
		return false
	}
	previous := ""
	for _, testCase := range scorecard.Cases {
		if testCase.CaseID <= previous || !validReportText(testCase.CaseID, 200) ||
			!validReportText(testCase.Question, 2048) || testCase.QualityStatus != QualityNotScored ||
			testCase.TotalTokens != testCase.InputTokens+testCase.OutputTokens || testCase.CachedTokens > testCase.InputTokens {
			return false
		}
		switch testCase.Status {
		case "succeeded":
			if !validReportText(testCase.Answer, 1<<20) || testCase.ErrorCode != "" ||
				testCase.ProviderCalls == 0 || testCase.MSQLCalls == 0 || testCase.ToolCalls == 0 {
				return false
			}
		case "failed":
			if testCase.Answer != "" || !validReportText(testCase.ErrorCode, 80) {
				return false
			}
		default:
			return false
		}
		previous = testCase.CaseID
	}
	return true
}

func validPrivateDiagnostics(diagnostics PrivateDiagnostics, sealed bool) bool {
	if diagnostics.Version != PrivateDiagnosticsVersion || !validReportText(diagnostics.RunID, 200) ||
		!validDigest(diagnostics.PublicScorecardSHA256) || diagnostics.Materialization.Validate() != nil ||
		len(diagnostics.Cases) == 0 || diagnostics.Cases == nil || sealed != validDigest(diagnostics.Hash) {
		return false
	}
	previous := ""
	for _, testCase := range diagnostics.Cases {
		task := testCase.Task
		if task.CaseID <= previous || task.Version != answercorpus.BlindTaskVersion ||
			!validReportText(task.CaseID, 200) || !validReportText(task.Question, 2048) ||
			task.CorpusID != diagnostics.Materialization.CorpusID ||
			task.CorpusRevision != diagnostics.Materialization.CorpusRevision ||
			task.SnapshotID != diagnostics.Materialization.FixtureSnapshotID ||
			task.SnapshotSHA256 != diagnostics.Materialization.FixtureSnapshotSHA256 ||
			len(task.AuthorizedDatabases) == 0 || testCase.Evidence == nil ||
			testCase.Trace.Validate() != nil || testCase.Trace.RunID != diagnostics.RunID ||
			testCase.Trace.SessionID != diagnostics.RunID+":"+task.CaseID {
			return false
		}
		if testCase.Error == "" {
			if len(testCase.Evidence) == 0 {
				return false
			}
		} else if !validReportText(testCase.Error, 4096) {
			return false
		}
		previous = task.CaseID
	}
	return true
}

func publicScorecardDigest(scorecard PublicScorecard) (string, error) {
	scorecard.Hash = ""
	return reportDigest(scorecard)
}

func privateDiagnosticsDigest(diagnostics PrivateDiagnostics) (string, error) {
	diagnostics.Hash = ""
	return reportDigest(diagnostics)
}

func reportDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validReportText(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && len(value) <= maximum &&
		!strings.ContainsRune(value, '\x00')
}
