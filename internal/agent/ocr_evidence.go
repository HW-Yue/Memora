package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

const OCREvidenceVersion = "memora.ocr-evidence/v1"

var (
	ErrOCREvidenceInvalid = errors.New("OCR evidence is invalid")
	ErrOCREvidenceBudget  = errors.New("OCR evidence exceeds its budget")
)

type OCREvidenceDecision string

const (
	OCREvidenceEligible OCREvidenceDecision = "eligible"
	OCREvidenceDeferred OCREvidenceDecision = "deferred"
)

const (
	OCREvidenceReasonEligible                 = "eligible"
	OCREvidenceReasonInsufficientScannedPages = "insufficient_scanned_pages"
	OCREvidenceReasonInsufficientSamples      = "insufficient_samples"
	OCREvidenceReasonRecallGainBelowThreshold = "recall_gain_below_threshold"
	OCREvidenceReasonLatencyBudgetExceeded    = "latency_budget_exceeded"
)

type OCREvidenceSample struct {
	Page                  uint64 `json:"page"`
	TextLayer             bool   `json:"text_layer"`
	BaselineCorrect       bool   `json:"baseline_correct"`
	OCRCorrect            bool   `json:"ocr_correct"`
	BaselineLatencyMillis uint64 `json:"baseline_latency_ms"`
	OCRLatencyMillis      uint64 `json:"ocr_latency_ms"`
}

type OCREvidenceConfig struct {
	MaxSamples                      uint64 `json:"max_samples"`
	MinScannedPages                 uint64 `json:"min_scanned_pages"`
	MinSamples                      uint64 `json:"min_samples"`
	MinRecallGainBasisPoints        uint64 `json:"min_recall_gain_basis_points"`
	MaxAverageLatencyIncreaseMillis uint64 `json:"max_average_latency_increase_ms"`
}

func DefaultOCREvidenceConfig() OCREvidenceConfig {
	return OCREvidenceConfig{
		MaxSamples: 100_000, MinScannedPages: 32, MinSamples: 32,
		MinRecallGainBasisPoints: 500, MaxAverageLatencyIncreaseMillis: 500,
	}
}

type OCREvidenceReport struct {
	Version                      string              `json:"version"`
	CorpusSHA256                 string              `json:"corpus_sha256"`
	Samples                      uint64              `json:"samples"`
	ScannedPages                 uint64              `json:"scanned_pages"`
	TextLayerPages               uint64              `json:"text_layer_pages"`
	BaselineCorrect              uint64              `json:"baseline_correct"`
	OCRCorrect                   uint64              `json:"ocr_correct"`
	RecallGainBasisPoints        int64               `json:"recall_gain_basis_points"`
	AverageLatencyIncreaseMillis uint64              `json:"average_latency_increase_ms"`
	Decision                     OCREvidenceDecision `json:"decision"`
	Reason                       string              `json:"reason"`
	SHA256                       string              `json:"sha256"`
}

func (report OCREvidenceReport) Validate() error {
	if report.Version != OCREvidenceVersion || !validSourceSHA256(report.CorpusSHA256) ||
		report.Decision != OCREvidenceEligible && report.Decision != OCREvidenceDeferred ||
		!validOCREvidenceReason(report.Reason) || !validSourceSHA256(report.SHA256) {
		return ErrOCREvidenceInvalid
	}
	if report.BaselineCorrect > report.Samples || report.OCRCorrect > report.Samples || report.TextLayerPages > report.Samples ||
		report.ScannedPages > report.Samples {
		return ErrOCREvidenceInvalid
	}
	digest, err := ocrEvidenceDigest(report)
	if err != nil || digest != report.SHA256 {
		return ErrOCREvidenceInvalid
	}
	return nil
}

func EvaluateOCREvidence(corpusSHA256 string, samples []OCREvidenceSample, config OCREvidenceConfig) (OCREvidenceReport, error) {
	if !validSourceSHA256(corpusSHA256) || !validOCREvidenceConfig(config) {
		return OCREvidenceReport{}, ErrOCREvidenceInvalid
	}
	if uint64(len(samples)) > config.MaxSamples {
		return OCREvidenceReport{}, ErrOCREvidenceBudget
	}
	seen := make(map[uint64]struct{}, len(samples))
	report := OCREvidenceReport{Version: OCREvidenceVersion, CorpusSHA256: corpusSHA256, Samples: uint64(len(samples))}
	var baselineLatency, ocrLatency uint64
	for _, sample := range samples {
		if sample.Page == 0 {
			return OCREvidenceReport{}, ErrOCREvidenceInvalid
		}
		if _, duplicate := seen[sample.Page]; duplicate {
			return OCREvidenceReport{}, ErrOCREvidenceInvalid
		}
		seen[sample.Page] = struct{}{}
		if sample.TextLayer {
			report.TextLayerPages++
		} else {
			report.ScannedPages++
		}
		if sample.BaselineCorrect {
			report.BaselineCorrect++
		}
		if sample.OCRCorrect {
			report.OCRCorrect++
		}
		if ^uint64(0)-baselineLatency < sample.BaselineLatencyMillis || ^uint64(0)-ocrLatency < sample.OCRLatencyMillis {
			return OCREvidenceReport{}, ErrOCREvidenceBudget
		}
		baselineLatency += sample.BaselineLatencyMillis
		ocrLatency += sample.OCRLatencyMillis
	}
	if report.Samples > 0 {
		baselineRate := report.BaselineCorrect * 10_000 / report.Samples
		ocrRate := report.OCRCorrect * 10_000 / report.Samples
		report.RecallGainBasisPoints = int64(ocrRate) - int64(baselineRate)
		if ocrLatency > baselineLatency {
			report.AverageLatencyIncreaseMillis = (ocrLatency - baselineLatency) / report.Samples
		}
	}
	report.Decision, report.Reason = OCREvidenceDeferred, OCREvidenceReasonInsufficientScannedPages
	switch {
	case report.ScannedPages < config.MinScannedPages:
		report.Reason = OCREvidenceReasonInsufficientScannedPages
	case report.Samples < config.MinSamples:
		report.Reason = OCREvidenceReasonInsufficientSamples
	case report.RecallGainBasisPoints < int64(config.MinRecallGainBasisPoints):
		report.Reason = OCREvidenceReasonRecallGainBelowThreshold
	case report.AverageLatencyIncreaseMillis > config.MaxAverageLatencyIncreaseMillis:
		report.Reason = OCREvidenceReasonLatencyBudgetExceeded
	default:
		report.Decision, report.Reason = OCREvidenceEligible, OCREvidenceReasonEligible
	}
	report.SHA256, _ = ocrEvidenceDigest(report)
	if err := report.Validate(); err != nil {
		return OCREvidenceReport{}, err
	}
	return report, nil
}

func validOCREvidenceConfig(config OCREvidenceConfig) bool {
	return config.MaxSamples > 0 && config.MinScannedPages > 0 && config.MinSamples > 0 &&
		config.MinSamples <= config.MaxSamples && config.MinRecallGainBasisPoints <= 10_000
}

func validOCREvidenceReason(reason string) bool {
	switch reason {
	case OCREvidenceReasonEligible, OCREvidenceReasonInsufficientScannedPages, OCREvidenceReasonInsufficientSamples,
		OCREvidenceReasonRecallGainBelowThreshold, OCREvidenceReasonLatencyBudgetExceeded:
		return true
	default:
		return false
	}
}

func ocrEvidenceDigest(report OCREvidenceReport) (string, error) {
	payload := struct {
		Version                                                            string `json:"version"`
		CorpusSHA256                                                       string `json:"corpus_sha256"`
		Samples, ScannedPages, TextLayerPages, BaselineCorrect, OCRCorrect uint64
		RecallGainBasisPoints                                              int64               `json:"recall_gain_basis_points"`
		AverageLatencyIncreaseMillis                                       uint64              `json:"average_latency_increase_ms"`
		Decision                                                           OCREvidenceDecision `json:"decision"`
		Reason                                                             string              `json:"reason"`
	}{report.Version, report.CorpusSHA256, report.Samples, report.ScannedPages, report.TextLayerPages,
		report.BaselineCorrect, report.OCRCorrect, report.RecallGainBasisPoints, report.AverageLatencyIncreaseMillis,
		report.Decision, report.Reason}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (report OCREvidenceReport) String() string {
	return fmt.Sprintf("%s/%s", report.Decision, report.Reason)
}
