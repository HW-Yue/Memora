package routetrace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Version       = "memora.route-trace/v1"
	MaxSteps      = 64
	MaxCandidates = 24
	MaxLocators   = 24
	maxTraceBytes = 256 << 10
	maxRetention  = 30 * 24 * time.Hour
)

var (
	ErrInvalid         = errors.New("Route Trace is invalid")
	ErrConflict        = errors.New("Route Trace conflicts with an existing receipt")
	ErrCorrupt         = errors.New("Route Trace store is corrupt")
	ErrNotFound        = errors.New("Route Trace was not found")
	ErrSnapshotExpired = errors.New("Route Trace snapshot expired after retention cleanup")
)

type Status string

const (
	StatusComplete     Status = "complete"
	StatusPartial      Status = "partial"
	StatusNoResults    Status = "no_results"
	StatusAccessDenied Status = "access_denied"
	StatusFailed       Status = "failed"
)

type Operation string

const (
	OperationFrame    Operation = "FRAME"
	OperationChoose   Operation = "CHOOSE"
	OperationOpen     Operation = "OPEN"
	OperationSelect   Operation = "SELECT"
	OperationFallback Operation = "FALLBACK"
)

type Outcome string

const (
	OutcomeSucceeded        Outcome = "succeeded"
	OutcomeNotFound         Outcome = "not_found"
	OutcomePermissionDenied Outcome = "permission_denied"
	OutcomeRevisionConflict Outcome = "revision_conflict"
	OutcomeTruncated        Outcome = "output_truncated"
	OutcomeCancelled        Outcome = "cancelled"
	OutcomeFailed           Outcome = "failed"
)

type Locator struct {
	RowID    string `json:"row_id"`
	Revision uint64 `json:"revision"`
}

type Step struct {
	Ordinal           uint64    `json:"ordinal"`
	Operation         Operation `json:"operation"`
	ParentRouteID     string    `json:"parent_route_id,omitempty"`
	CandidateRouteIDs []string  `json:"candidate_route_ids"`
	SelectedRouteID   string    `json:"selected_route_id,omitempty"`
	Locators          []Locator `json:"locators"`
	Outcome           Outcome   `json:"outcome"`
	ElapsedMillis     uint64    `json:"elapsed_ms"`
	RemainingBudget   uint64    `json:"remaining_budget"`
}

type Draft struct {
	TraceID    string    `json:"trace_id"`
	RecordedAt time.Time `json:"recorded_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Actor      string    `json:"actor"`
	DatabaseID string    `json:"database_id"`
	TableID    string    `json:"table_id"`
	Status     Status    `json:"status"`
	Steps      []Step    `json:"steps"`
}

type Trace struct {
	Version    string    `json:"version"`
	TraceID    string    `json:"trace_id"`
	Sequence   uint64    `json:"trace_sequence"`
	RecordedAt time.Time `json:"recorded_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Actor      string    `json:"actor"`
	DatabaseID string    `json:"database_id"`
	TableID    string    `json:"table_id"`
	Status     Status    `json:"status"`
	Steps      []Step    `json:"steps"`
	Checksum   string    `json:"checksum"`
}

type Snapshot struct {
	HighWater      uint64
	RetentionEpoch uint64
}

func (value Trace) Validate() error {
	if value.Version != Version || value.Sequence == 0 || len(value.Checksum) != 64 {
		return ErrInvalid
	}
	draft := Draft{
		TraceID: value.TraceID, RecordedAt: value.RecordedAt, ExpiresAt: value.ExpiresAt,
		Actor: value.Actor, DatabaseID: value.DatabaseID, TableID: value.TableID,
		Status: value.Status, Steps: value.Steps,
	}
	if err := draft.validate(); err != nil {
		return err
	}
	want, err := traceChecksum(value)
	if err != nil || want != value.Checksum {
		return fmt.Errorf("%w: checksum", ErrInvalid)
	}
	return nil
}

func (value Draft) validate() error {
	if !validTraceID(value.TraceID) || value.RecordedAt.IsZero() || value.ExpiresAt.IsZero() ||
		value.RecordedAt.Location() != time.UTC || value.ExpiresAt.Location() != time.UTC ||
		!value.ExpiresAt.After(value.RecordedAt) || value.ExpiresAt.Sub(value.RecordedAt) > maxRetention ||
		!validText(value.Actor, 160) || !validStableID(value.DatabaseID, "db_") ||
		!validStableID(value.TableID, "tbl_") || !validStatus(value.Status) ||
		len(value.Steps) == 0 || len(value.Steps) > MaxSteps {
		return ErrInvalid
	}
	for index, step := range value.Steps {
		if err := validateStep(step, uint64(index+1)); err != nil {
			return err
		}
	}
	return nil
}

func validateStep(value Step, ordinal uint64) error {
	if value.Ordinal != ordinal || !validOperation(value.Operation) || !validOutcome(value.Outcome) ||
		!validOptionalStableID(value.ParentRouteID, "route_") ||
		!validOptionalStableID(value.SelectedRouteID, "route_") ||
		value.CandidateRouteIDs == nil || value.Locators == nil ||
		len(value.CandidateRouteIDs) > MaxCandidates || len(value.Locators) > MaxLocators ||
		value.ElapsedMillis > uint64((24*time.Hour)/time.Millisecond) || value.RemainingBudget > 10_000 {
		return ErrInvalid
	}
	seen := make(map[string]bool, len(value.CandidateRouteIDs))
	for _, id := range value.CandidateRouteIDs {
		if !validStableID(id, "route_") || seen[id] {
			return ErrInvalid
		}
		seen[id] = true
	}
	if value.SelectedRouteID != "" && len(value.CandidateRouteIDs) > 0 && !seen[value.SelectedRouteID] {
		return ErrInvalid
	}
	rows := make(map[string]bool, len(value.Locators))
	for _, locator := range value.Locators {
		if !validStableID(locator.RowID, "row_") || locator.Revision == 0 || rows[locator.RowID] {
			return ErrInvalid
		}
		rows[locator.RowID] = true
	}
	return nil
}

func seal(draft Draft, sequence uint64) (Trace, error) {
	draft = cloneDraft(draft)
	if sequence == 0 || draft.validate() != nil {
		return Trace{}, ErrInvalid
	}
	value := Trace{
		Version: Version, TraceID: draft.TraceID, Sequence: sequence,
		RecordedAt: draft.RecordedAt, ExpiresAt: draft.ExpiresAt, Actor: draft.Actor,
		DatabaseID: draft.DatabaseID, TableID: draft.TableID, Status: draft.Status, Steps: draft.Steps,
	}
	checksum, err := traceChecksum(value)
	if err != nil {
		return Trace{}, err
	}
	value.Checksum = checksum
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxTraceBytes {
		return Trace{}, ErrInvalid
	}
	return value, nil
}

func traceChecksum(value Trace) (string, error) {
	payload := struct {
		Version    string    `json:"version"`
		TraceID    string    `json:"trace_id"`
		Sequence   uint64    `json:"trace_sequence"`
		RecordedAt time.Time `json:"recorded_at"`
		ExpiresAt  time.Time `json:"expires_at"`
		Actor      string    `json:"actor"`
		DatabaseID string    `json:"database_id"`
		TableID    string    `json:"table_id"`
		Status     Status    `json:"status"`
		Steps      []Step    `json:"steps"`
	}{
		value.Version, value.TraceID, value.Sequence, value.RecordedAt, value.ExpiresAt,
		value.Actor, value.DatabaseID, value.TableID, value.Status, value.Steps,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneDraft(value Draft) Draft {
	value.Steps = append([]Step(nil), value.Steps...)
	for index := range value.Steps {
		value.Steps[index].CandidateRouteIDs = append([]string{}, value.Steps[index].CandidateRouteIDs...)
		value.Steps[index].Locators = append([]Locator{}, value.Steps[index].Locators...)
	}
	return value
}

func validTraceID(value string) bool {
	if len(value) != 38 || !strings.HasPrefix(value, "trace_") || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value[6:])
	return err == nil && len(decoded) == 16
}

func validText(value string, maximum int) bool {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len([]rune(value)) > maximum {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validStableID(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && validText(value, 200) && !strings.ContainsAny(value, " \t\r\n")
}

func validOptionalStableID(value, prefix string) bool {
	return value == "" || validStableID(value, prefix)
}

func validStatus(value Status) bool {
	switch value {
	case StatusComplete, StatusPartial, StatusNoResults, StatusAccessDenied, StatusFailed:
		return true
	default:
		return false
	}
}

func validOperation(value Operation) bool {
	switch value {
	case OperationFrame, OperationChoose, OperationOpen, OperationSelect, OperationFallback:
		return true
	default:
		return false
	}
}

func validOutcome(value Outcome) bool {
	switch value {
	case OutcomeSucceeded, OutcomeNotFound, OutcomePermissionDenied, OutcomeRevisionConflict,
		OutcomeTruncated, OutcomeCancelled, OutcomeFailed:
		return true
	default:
		return false
	}
}
