package assimilation

import (
	"fmt"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillwrite"
)

const (
	SubmissionVersion    = "memora.assimilation-submission/v1"
	ReviewVersion        = "memora.assimilation-review/v1"
	SourceReceiptVersion = "memora.source-receipt/v1"
)

type ReviewVerdict string

const (
	ReviewAccepted ReviewVerdict = "accepted"
	ReviewRejected ReviewVerdict = "rejected"
)

type SourceAnchor struct {
	UnitID string `json:"unit_id"`
	Start  uint64 `json:"start"`
	End    uint64 `json:"end"`
}

type SemanticModule struct {
	ID      string          `json:"id"`
	Anchors []SourceAnchor  `json:"anchors"`
	Plan    skillwrite.Plan `json:"plan"`
}

type SemanticRelationship struct {
	ID             string          `json:"id"`
	SourceModuleID string          `json:"source_module_id"`
	TargetModuleID string          `json:"target_module_id"`
	Anchors        []SourceAnchor  `json:"anchors"`
	Plan           skillwrite.Plan `json:"plan"`
}

type KeyFact struct {
	ID          string       `json:"id"`
	ModuleID    string       `json:"module_id"`
	Field       string       `json:"field"`
	ValueDigest string       `json:"value_digest"`
	Anchor      SourceAnchor `json:"anchor"`
}

type Review struct {
	Version                string        `json:"version"`
	Reviewer               string        `json:"reviewer"`
	ContextID              string        `json:"context_id"`
	DraftDigest            string        `json:"draft_digest"`
	CoverageRevision       uint64        `json:"coverage_revision"`
	Verdict                ReviewVerdict `json:"verdict"`
	CheckedModuleIDs       []string      `json:"checked_module_ids"`
	CheckedRelationshipIDs []string      `json:"checked_relationship_ids"`
	CheckedKeyFactIDs      []string      `json:"checked_key_fact_ids"`
	AnchorsVerified        bool          `json:"anchors_verified"`
	KeyFactsVerified       bool          `json:"key_facts_verified"`
	ConflictsChecked       bool          `json:"conflicts_checked"`
	RawContentAbsent       bool          `json:"raw_content_absent"`
}

type Submission struct {
	Version               string                 `json:"version"`
	SubmissionID          string                 `json:"submission_id"`
	TaskID                string                 `json:"task_id"`
	Workspace             string                 `json:"workspace"`
	CoverageRevision      uint64                 `json:"coverage_revision"`
	Author                string                 `json:"author"`
	DraftContextID        string                 `json:"draft_context_id"`
	DraftDigest           string                 `json:"draft_digest"`
	Modules               []SemanticModule       `json:"modules"`
	Relationships         []SemanticRelationship `json:"relationships"`
	KeyFacts              []KeyFact              `json:"key_facts"`
	Review                Review                 `json:"review"`
	UnresolvedConflictIDs []string               `json:"unresolved_conflict_ids"`
}

type SubmissionStatus string

const (
	SubmissionCommitted SubmissionStatus = "committed"
	SubmissionNeedsUser SubmissionStatus = "needs_user"
	SubmissionInDoubt   SubmissionStatus = "in_doubt"
)

type SourceImpact struct {
	Kind     string                   `json:"kind"`
	ID       string                   `json:"id"`
	PlanID   string                   `json:"plan_id"`
	Decision skillwrite.Decision      `json:"decision"`
	Status   skillwrite.ReceiptStatus `json:"status"`
	Changes  []skillwrite.Change      `json:"changes"`
}

type KeyFactReceipt struct {
	ID          string       `json:"id"`
	ModuleID    string       `json:"module_id"`
	Field       string       `json:"field"`
	ValueDigest string       `json:"value_digest"`
	Anchor      SourceAnchor `json:"anchor"`
}

type SourceReceipt struct {
	Version               string           `json:"version"`
	SubmissionID          string           `json:"submission_id"`
	TaskID                string           `json:"task_id"`
	Workspace             string           `json:"workspace"`
	Status                SubmissionStatus `json:"status"`
	Replayed              bool             `json:"replayed"`
	Source                Source           `json:"source"`
	CoverageRevision      uint64           `json:"coverage_revision"`
	Author                string           `json:"author"`
	Reviewer              string           `json:"reviewer"`
	Impacts               []SourceImpact   `json:"impacts"`
	KeyFacts              []KeyFactReceipt `json:"key_facts"`
	UnresolvedConflictIDs []string         `json:"unresolved_conflict_ids,omitempty"`
	Warnings              []result.Notice  `json:"warnings"`
}

type SubmissionError struct {
	Code    result.Code
	Message string
}

func (err *SubmissionError) Error() string      { return "assimilation submission: " + err.Message }
func (err *SubmissionError) StableCode() string { return string(err.Code) }

func submissionError(code result.Code, format string, arguments ...any) error {
	return &SubmissionError{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
