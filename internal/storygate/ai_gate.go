package storygate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/HW-Yue/Memora/internal/hostcontract"
)

const AIReportVersion = "memora.ai-story-gate/v2"
const AIEvidenceSetVersion = "memora.ai-story-evidence-set/v1"

type OutcomeCheck struct {
	Name           string `json:"name"`
	Passed         bool   `json:"passed"`
	EvidenceDigest string `json:"evidence_digest"`
}

type AIJourneyEvidence struct {
	Story      string               `json:"story"`
	TaskDigest string               `json:"task_digest"`
	Receipt    hostcontract.Receipt `json:"receipt"`
	Checks     []OutcomeCheck       `json:"checks"`
}

type RuntimeBinding struct {
	Host         string `json:"host"`
	ReportDigest string `json:"report_digest"`
}

type AIReport struct {
	Version         string              `json:"version"`
	Status          string              `json:"status"`
	SuiteDigest     string              `json:"suite_digest"`
	ContractDigest  string              `json:"contract_digest"`
	BinarySHA256    string              `json:"binary_sha256"`
	CanonicalDigest string              `json:"canonical_digest"`
	ProtocolDigest  string              `json:"protocol_digest"`
	Runtime         []RuntimeBinding    `json:"runtime"`
	Journeys        []AIJourneyEvidence `json:"journeys"`
	Hash            string              `json:"hash"`
}

type AIEvidenceSet struct {
	Version        string              `json:"version"`
	SuiteDigest    string              `json:"suite_digest"`
	ContractDigest string              `json:"contract_digest"`
	Journeys       []AIJourneyEvidence `json:"journeys"`
}

func BuildAIReport(
	contract hostcontract.Contract,
	contractDigest string,
	suite AIJourneySuite,
	codex Report,
	claude Report,
	evidence []AIJourneyEvidence,
) (AIReport, error) {
	if err := ValidatePair(codex, claude); err != nil {
		return AIReport{}, err
	}
	if err := suite.Validate(); err != nil {
		return AIReport{}, err
	}
	if contractDigest != codex.TaskContractDigest || contractDigest != claude.TaskContractDigest {
		return AIReport{}, fmt.Errorf("AI story contract digest differs from runtime prerequisite")
	}
	ordered, err := validateAndOrderJourneys(contract, suite, codex, evidence)
	if err != nil {
		return AIReport{}, err
	}
	codexDigest, err := mechanicalReportDigest(codex)
	if err != nil {
		return AIReport{}, err
	}
	claudeDigest, err := mechanicalReportDigest(claude)
	if err != nil {
		return AIReport{}, err
	}
	report := AIReport{
		Version: AIReportVersion, Status: "passed", SuiteDigest: suite.Hash,
		ContractDigest: contractDigest, BinarySHA256: codex.BinarySHA256,
		CanonicalDigest: codex.CanonicalDigest, ProtocolDigest: codex.ProtocolDigest,
		Runtime: []RuntimeBinding{{Host: "codex", ReportDigest: codexDigest},
			{Host: "claude", ReportDigest: claudeDigest}},
		Journeys: ordered,
	}
	hash, err := aiReportDigest(report)
	if err != nil {
		return AIReport{}, err
	}
	report.Hash = hash
	if err := report.ValidateAgainst(contract, contractDigest, suite, codex, claude); err != nil {
		return AIReport{}, err
	}
	return report, nil
}

func (report AIReport) ValidateAgainst(
	contract hostcontract.Contract,
	contractDigest string,
	suite AIJourneySuite,
	codex Report,
	claude Report,
) error {
	if err := report.Validate(); err != nil {
		return err
	}
	if err := ValidatePair(codex, claude); err != nil {
		return err
	}
	if err := suite.Validate(); err != nil {
		return err
	}
	codexDigest, _ := mechanicalReportDigest(codex)
	claudeDigest, _ := mechanicalReportDigest(claude)
	if report.SuiteDigest != suite.Hash || report.ContractDigest != contractDigest ||
		contractDigest != codex.TaskContractDigest || report.BinarySHA256 != codex.BinarySHA256 ||
		report.CanonicalDigest != codex.CanonicalDigest || report.ProtocolDigest != codex.ProtocolDigest ||
		!reflect.DeepEqual(report.Runtime, []RuntimeBinding{{"codex", codexDigest}, {"claude", claudeDigest}}) {
		return fmt.Errorf("AI story report differs from Suite, contract, or runtime prerequisite")
	}
	ordered, err := validateAndOrderJourneys(contract, suite, codex, report.Journeys)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(ordered, report.Journeys) {
		return fmt.Errorf("AI story journeys are not in canonical order")
	}
	return nil
}

func (report AIReport) Validate() error {
	if !isSHA256(report.Hash) {
		return fmt.Errorf("AI story report hash is invalid")
	}
	want, err := aiReportDigest(report)
	if err != nil {
		return err
	}
	if report.Hash != want {
		return fmt.Errorf("AI story report hash mismatch")
	}
	if report.Version != AIReportVersion || report.Status != "passed" ||
		!isSHA256(report.SuiteDigest) || !isSHA256(report.ContractDigest) ||
		!isSHA256(report.BinarySHA256) || !isSHA256(report.CanonicalDigest) ||
		!isSHA256(report.ProtocolDigest) || len(report.Runtime) != 2 ||
		len(report.Journeys) != 2*len(RequiredStories()) {
		return fmt.Errorf("AI story report identity or evidence count is invalid")
	}
	if report.Runtime[0].Host != "codex" || report.Runtime[1].Host != "claude" ||
		!isSHA256(report.Runtime[0].ReportDigest) || !isSHA256(report.Runtime[1].ReportDigest) {
		return fmt.Errorf("AI story runtime bindings are invalid")
	}
	stories := map[string]bool{}
	for _, story := range RequiredStories() {
		stories[story] = true
	}
	seen := map[string]bool{}
	for _, journey := range report.Journeys {
		key := journey.Story + "\x00" + journey.Receipt.Host.Surface
		if !stories[journey.Story] ||
			(journey.Receipt.Host.Surface != "codex" && journey.Receipt.Host.Surface != "claude-code") ||
			seen[key] || !isSHA256(journey.TaskDigest) || journey.Receipt.TaskDigest != journey.TaskDigest ||
			journey.Receipt.Status != "completed" || len(journey.Receipt.ToolCalls) == 0 || journey.Checks == nil {
			return fmt.Errorf("AI story journey identity is invalid or duplicated")
		}
		seen[key] = true
		for _, check := range journey.Checks {
			if !validCheckName(check.Name) || !check.Passed || !isSHA256(check.EvidenceDigest) {
				return fmt.Errorf("AI story journey outcome check is invalid")
			}
		}
	}
	return nil
}

func EncodeAIReport(report AIReport) ([]byte, error) {
	if err := report.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode AI story report: %w", err)
	}
	return append(encoded, '\n'), nil
}

func DecodeAIReport(encoded []byte) (AIReport, error) {
	var report AIReport
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return AIReport{}, fmt.Errorf("decode AI story report: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return AIReport{}, fmt.Errorf("decode AI story report: trailing JSON value")
	}
	if err := report.Validate(); err != nil {
		return AIReport{}, err
	}
	return report, nil
}

func LoadAIReport(path string) (AIReport, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return AIReport{}, fmt.Errorf("read AI story report: %w", err)
	}
	return DecodeAIReport(encoded)
}

func DecodeRuntimeReport(encoded []byte) (Report, error) {
	var report Report
	if err := decodeStrictJSON(encoded, &report); err != nil {
		return Report{}, fmt.Errorf("decode mechanical story report: %w", err)
	}
	if err := Validate(report); err != nil {
		return Report{}, err
	}
	return report, nil
}

func LoadRuntimeReport(path string) (Report, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return Report{}, fmt.Errorf("read mechanical story report: %w", err)
	}
	return DecodeRuntimeReport(encoded)
}

func DecodeAIEvidenceSet(encoded []byte) (AIEvidenceSet, error) {
	var evidence AIEvidenceSet
	if err := decodeStrictJSON(encoded, &evidence); err != nil {
		return AIEvidenceSet{}, fmt.Errorf("decode AI story evidence: %w", err)
	}
	if evidence.Version != AIEvidenceSetVersion || !isSHA256(evidence.SuiteDigest) ||
		!isSHA256(evidence.ContractDigest) || evidence.Journeys == nil {
		return AIEvidenceSet{}, fmt.Errorf("AI story evidence identity is invalid")
	}
	return evidence, nil
}

func LoadAIEvidenceSet(path string) (AIEvidenceSet, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return AIEvidenceSet{}, fmt.Errorf("read AI story evidence: %w", err)
	}
	return DecodeAIEvidenceSet(encoded)
}

func WriteAIReportAtomic(path string, report AIReport) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("AI story report path must be absolute and normalized")
	}
	encoded, err := EncodeAIReport(report)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create AI story report directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".ai-story-gate-*.json")
	if err != nil {
		return fmt.Errorf("create AI story report staging: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func decodeStrictJSON(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing JSON value")
	}
	return nil
}

func validateAndOrderJourneys(
	contract hostcontract.Contract,
	suite AIJourneySuite,
	runtimeReport Report,
	evidence []AIJourneyEvidence,
) ([]AIJourneyEvidence, error) {
	byKey := map[string]AIJourneyEvidence{}
	for _, journey := range evidence {
		key := journey.Story + "\x00" + journey.Receipt.Host.Surface
		if byKey[key].Story != "" {
			return nil, fmt.Errorf("AI story journey %s is duplicated", key)
		}
		byKey[key] = journey
	}
	ordered := make([]AIJourneyEvidence, 0, 2*len(suite.Stories))
	for _, specification := range suite.Stories {
		task, err := suite.Task(specification.Story)
		if err != nil {
			return nil, err
		}
		taskDigest, err := contract.TaskDigest(task)
		if err != nil {
			return nil, err
		}
		for _, surface := range []string{"codex", "claude-code"} {
			journey, ok := byKey[specification.Story+"\x00"+surface]
			if !ok {
				return nil, fmt.Errorf("AI story %s is missing %s evidence", specification.Story, surface)
			}
			if journey.TaskDigest != taskDigest || journey.Receipt.TaskDigest != taskDigest ||
				journey.Receipt.SkillDigest != runtimeReport.CanonicalDigest ||
				journey.Receipt.ProtocolDigest != runtimeReport.ProtocolDigest ||
				journey.Receipt.Status != "completed" || len(journey.Receipt.ToolCalls) == 0 {
				return nil, fmt.Errorf("AI story %s %s receipt identity or status is invalid", specification.Story, surface)
			}
			if err := contract.ValidateReceipt(task, journey.Receipt); err != nil {
				return nil, fmt.Errorf("AI story %s %s receipt: %w", specification.Story, surface, err)
			}
			if err := validateChecks(specification.RequiredChecks, journey.Checks); err != nil {
				return nil, fmt.Errorf("AI story %s %s check: %w", specification.Story, surface, err)
			}
			ordered = append(ordered, journey)
			delete(byKey, specification.Story+"\x00"+surface)
		}
	}
	if len(byKey) > 0 {
		keys := make([]string, 0, len(byKey))
		for key := range byKey {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("AI story evidence contains unknown journey %s", keys[0])
	}
	return ordered, nil
}

func validateChecks(required []string, checks []OutcomeCheck) error {
	if len(checks) != len(required) {
		return fmt.Errorf("required outcome check count differs")
	}
	for index, check := range checks {
		if check.Name != required[index] || !check.Passed || !isSHA256(check.EvidenceDigest) {
			return fmt.Errorf("required outcome check %d failed or drifted", index)
		}
	}
	return nil
}

func mechanicalReportDigest(report Report) (string, error) {
	encoded, err := json.Marshal(report)
	if err != nil {
		return "", fmt.Errorf("encode mechanical story report: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func aiReportDigest(report AIReport) (string, error) {
	report.Hash = ""
	encoded, err := json.Marshal(report)
	if err != nil {
		return "", fmt.Errorf("encode AI story report identity: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
