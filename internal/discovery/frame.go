package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

const Version = "memora.discovery-frame/v1"

const UsageNavigationOnly = "navigation_only"

var ErrInvalidFrame = errors.New("invalid discovery frame")

type PredictorStatus string

const (
	PredictorSucceeded   PredictorStatus = "succeeded"
	PredictorUnavailable PredictorStatus = "unavailable"
)

type ScoreKind string

const (
	ScoreNone       ScoreKind = "none"
	ScoreMatchCount ScoreKind = "match_count"
	ScoreDotProduct ScoreKind = "dot_product"
)

type Budget struct {
	CandidateLimit uint64 `json:"candidate_limit"`
	UTF8ByteLimit  uint64 `json:"utf8_byte_limit"`
	CandidatesUsed uint64 `json:"candidates_used"`
	UTF8BytesUsed  uint64 `json:"utf8_bytes_used"`
}

type PredictorReceipt struct {
	Predictor      string          `json:"predictor"`
	Status         PredictorStatus `json:"status"`
	ScoreKind      ScoreKind       `json:"score_kind"`
	Reason         string          `json:"reason"`
	CandidateCount uint64          `json:"candidate_count"`
	Truncated      bool            `json:"truncated"`
}

type Candidate struct {
	DatabaseID    string    `json:"database_id"`
	TableID       string    `json:"table_id,omitempty"`
	RouteID       string    `json:"route_id,omitempty"`
	RouteRevision uint64    `json:"route_revision,omitempty"`
	Predictor     string    `json:"predictor"`
	Reason        string    `json:"reason"`
	ScoreKind     ScoreKind `json:"score_kind"`
	Score         *float64  `json:"score,omitempty"`
}

type Frame struct {
	Version         string             `json:"version"`
	Usage           string             `json:"usage"`
	Snapshot        string             `json:"snapshot"`
	CatalogRevision string             `json:"catalog_revision"`
	Budget          Budget             `json:"budget"`
	Predictors      []PredictorReceipt `json:"predictors"`
	Candidates      []Candidate        `json:"candidates"`
	Truncated       bool               `json:"truncated"`
}

func (frame Frame) Validate() error {
	if frame.Version != Version || frame.Usage != UsageNavigationOnly {
		return invalid("unsupported version or usage")
	}
	if !validOpaque(frame.Snapshot) || !validOpaque(frame.CatalogRevision) {
		return invalid("snapshot and catalog_revision are required")
	}
	if frame.Predictors == nil || frame.Candidates == nil {
		return invalid("predictors and candidates must be arrays")
	}
	if err := validateBudget(frame.Budget); err != nil {
		return err
	}
	if frame.Budget.CandidatesUsed != uint64(len(frame.Candidates)) {
		return invalid("candidates_used does not match candidates")
	}

	receipts := make(map[string]PredictorReceipt, len(frame.Predictors))
	truncated := false
	for _, receipt := range frame.Predictors {
		if err := validateReceipt(receipt); err != nil {
			return err
		}
		if _, exists := receipts[receipt.Predictor]; exists {
			return invalid("predictor %q has multiple receipts", receipt.Predictor)
		}
		receipts[receipt.Predictor] = receipt
		truncated = truncated || receipt.Truncated
	}
	if frame.Truncated != truncated {
		return invalid("frame truncation does not match predictor receipts")
	}

	counts := make(map[string]uint64, len(receipts))
	locations := make(map[string]struct{}, len(frame.Candidates))
	var encodedBytes uint64
	for _, candidate := range frame.Candidates {
		if err := validateCandidate(candidate); err != nil {
			return err
		}
		receipt, exists := receipts[candidate.Predictor]
		if !exists || receipt.Status != PredictorSucceeded || receipt.ScoreKind != candidate.ScoreKind {
			return invalid("candidate has no matching successful predictor receipt")
		}
		key := candidate.Predictor + "\x00" + candidate.DatabaseID + "\x00" + candidate.TableID + "\x00" + candidate.RouteID
		if _, exists := locations[key]; exists {
			return invalid("predictor %q repeats a candidate location", candidate.Predictor)
		}
		locations[key] = struct{}{}
		counts[candidate.Predictor]++
		size, err := candidateSize(candidate)
		if err != nil {
			return err
		}
		encodedBytes += size
	}
	if encodedBytes != frame.Budget.UTF8BytesUsed {
		return invalid("utf8_bytes_used does not match candidate encoding")
	}
	for predictor, receipt := range receipts {
		if receipt.CandidateCount != counts[predictor] {
			return invalid("predictor %q candidate_count is inconsistent", predictor)
		}
	}
	return nil
}

func (frame Frame) MarshalJSON() ([]byte, error) {
	if err := frame.Validate(); err != nil {
		return nil, err
	}
	type wire Frame
	return json.Marshal(wire(frame))
}

func (frame *Frame) UnmarshalJSON(data []byte) error {
	type wire Frame
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := Frame(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*frame = value
	return nil
}

var predictorPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]*(/v[1-9][0-9]*)?$`)

func validateBudget(value Budget) error {
	if value.CandidateLimit == 0 || value.CandidateLimit > 1024 ||
		value.UTF8ByteLimit == 0 || value.UTF8ByteLimit > 1024*1024 {
		return invalid("budget limits are outside protocol bounds")
	}
	if value.CandidatesUsed > value.CandidateLimit || value.UTF8BytesUsed > value.UTF8ByteLimit {
		return invalid("budget usage exceeds a hard limit")
	}
	return nil
}

func validateReceipt(value PredictorReceipt) error {
	if !validPredictor(value.Predictor) || !validReason(value.Reason) || !validScoreKind(value.ScoreKind) {
		return invalid("predictor receipt has invalid provenance")
	}
	switch value.Status {
	case PredictorSucceeded:
	case PredictorUnavailable:
		if value.CandidateCount != 0 || value.Truncated {
			return invalid("unavailable predictor cannot contain or truncate candidates")
		}
	default:
		return invalid("unknown predictor status %q", value.Status)
	}
	return nil
}

func validateCandidate(value Candidate) error {
	if !validID(value.DatabaseID) || !validPredictor(value.Predictor) || !validReason(value.Reason) ||
		!validScoreKind(value.ScoreKind) {
		return invalid("candidate has invalid identity or provenance")
	}
	if value.TableID != "" && !validID(value.TableID) {
		return invalid("candidate has invalid table_id")
	}
	if value.RouteID != "" && (!validID(value.RouteID) || value.TableID == "" || value.RouteRevision == 0) {
		return invalid("Route candidate requires a Table and revision")
	}
	if value.RouteID == "" && value.RouteRevision != 0 {
		return invalid("route_revision requires route_id")
	}
	if value.ScoreKind == ScoreNone {
		if value.Score != nil {
			return invalid("score_kind none cannot carry score")
		}
		return nil
	}
	if value.Score == nil || math.IsNaN(*value.Score) || math.IsInf(*value.Score, 0) {
		return invalid("scored candidate requires a finite score")
	}
	if value.ScoreKind == ScoreMatchCount && (*value.Score < 0 || math.Trunc(*value.Score) != *value.Score) {
		return invalid("match_count score must be a non-negative integer")
	}
	if value.ScoreKind == ScoreDotProduct && (*value.Score < -1.000001 || *value.Score > 1.000001) {
		return invalid("dot_product score is outside normalized bounds")
	}
	return nil
}

func validScoreKind(value ScoreKind) bool {
	return value == ScoreNone || value == ScoreMatchCount || value == ScoreDotProduct
}

func validPredictor(value string) bool {
	return len(value) <= 64 && predictorPattern.MatchString(value)
}

func validID(value string) bool {
	return value != "" && len(value) <= 128 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n\t")
}

func validOpaque(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func validReason(value string) bool {
	return value != "" && len(value) <= 512 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func candidateSize(candidate Candidate) (uint64, error) {
	type wire Candidate
	encoded, err := json.Marshal(wire(candidate))
	if err != nil {
		return 0, invalid("encode candidate: %v", err)
	}
	return uint64(len(encoded)), nil
}

func invalid(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidFrame, fmt.Sprintf(format, arguments...))
}
