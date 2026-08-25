package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Version is v2 because v1 carried scores, reasons, matched fields, predictor
// receipts and a four-part budget. Retrieval answers one question — where in
// the semantic tree the hit is — so all of that is gone rather than left
// present and unfilled: a field that is always empty is a lie a caller will
// eventually build on. See docs/query/predictor-path-only-v1.md.
const Version = "memora.discovery-frame/v2"

const UsageNavigationOnly = "navigation_only"

var ErrInvalidFrame = errors.New("invalid discovery frame")

// Candidate is one place in the semantic tree.
//
// DatabaseID and TableID stay because a path is relative to its Table. Path is
// the answer itself. Nothing else belongs here: a score, once exposed, becomes
// the authority the caller ranks by, and an explanation becomes a second
// authority beside the Router.
type Candidate struct {
	DatabaseID string `json:"database_id"`
	TableID    string `json:"table_id,omitempty"`
	Path       string `json:"path,omitempty"`
}

// Frame is the envelope for a candidate listing.
//
// Snapshot and CatalogRevision stay: they are not scores but the evidence of
// which view the batch was read from. Limit and Truncated stay because bounded
// output is a charter requirement — but only the limit, not a report of how
// much of it was consumed.
type Frame struct {
	Version         string      `json:"version"`
	Usage           string      `json:"usage"`
	Snapshot        string      `json:"snapshot"`
	CatalogRevision string      `json:"catalog_revision"`
	Limit           uint64      `json:"limit"`
	Candidates      []Candidate `json:"candidates"`
	Truncated       bool        `json:"truncated"`
}

const maxCandidateLimit = 1024

func (frame Frame) Validate() error {
	if frame.Version != Version || frame.Usage != UsageNavigationOnly {
		return invalid("unsupported version or usage")
	}
	if !validOpaque(frame.Snapshot) || !validOpaque(frame.CatalogRevision) {
		return invalid("snapshot and catalog_revision are required")
	}
	if frame.Candidates == nil {
		return invalid("candidates must be an array")
	}
	if frame.Limit == 0 || frame.Limit > maxCandidateLimit {
		return invalid("limit is outside protocol bounds")
	}
	if uint64(len(frame.Candidates)) > frame.Limit {
		return invalid("candidates exceed the limit")
	}
	for index, candidate := range frame.Candidates {
		if err := validateCandidate(candidate); err != nil {
			return err
		}
		if index == 0 {
			continue
		}
		// Sorted and unique by location. Ordering is part of the contract
		// because ranking is not: without a stable published order, callers
		// would read meaning into whatever order the predictor happened to
		// produce, which is the exposed score coming back in disguise.
		if !candidateLess(frame.Candidates[index-1], candidate) {
			return invalid("candidates must be sorted unique locations")
		}
	}
	return nil
}

// candidateLess is the published order: Database, then Table, then path in
// lexicographic byte order.
func candidateLess(left, right Candidate) bool {
	if left.DatabaseID != right.DatabaseID {
		return left.DatabaseID < right.DatabaseID
	}
	if left.TableID != right.TableID {
		return left.TableID < right.TableID
	}
	return left.Path < right.Path
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

func validateCandidate(value Candidate) error {
	if !validID(value.DatabaseID) {
		return invalid("candidate has invalid database_id")
	}
	if value.TableID != "" && !validID(value.TableID) {
		return invalid("candidate has invalid table_id")
	}
	if value.Path == "" {
		// A Database- or Table-level hit has no path inside a tree. A path
		// without a Table has nowhere to be relative to.
		return nil
	}
	if value.TableID == "" {
		return invalid("a path requires a table_id")
	}
	if !validPath(value.Path) {
		return invalid("candidate has invalid path")
	}
	return nil
}

func validID(value string) bool {
	return value != "" && len(value) <= 128 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n\t")
}

func validOpaque(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func validPath(value string) bool {
	return len(value) <= 1024 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n\t")
}

func invalid(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidFrame, fmt.Sprintf(format, arguments...))
}
