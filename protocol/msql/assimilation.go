package msql

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const (
	AssimilationProposalVersion = "memora.assimilation-proposal/v1"
	AssimilationPlanVersion     = "memora.assimilation-plan/v1"
	AssimilationReceiptVersion  = "memora.assimilation-receipt/v1"
	AssimilationEvidenceVersion = "memora.assimilation-evidence/v1"
	AssimilationPlanReview      = "review_required"
	AssimilationSubmitAction    = "SUBMIT_ASSIMILATION"
	AssimilationCommitted       = "committed"
	AssimilationInDoubt         = "in_doubt"
	MaxAssimilationStatements   = 64
	MaxAssimilationPlanBytes    = 512 << 10
)

type AssimilationStatement struct {
	MSQL       string          `json:"msql"`
	Parameters Parameters      `json:"parameters,omitempty"`
	Mutation   MutationOptions `json:"mutation"`
}

type AssimilationProposal struct {
	Version          string                  `json:"version"`
	ProposalID       string                  `json:"proposal_id"`
	Database         string                  `json:"database"`
	Author           string                  `json:"author"`
	SourceID         string                  `json:"source_id"`
	SourceLocator    string                  `json:"source_locator"`
	SourceSHA256     string                  `json:"source_sha256"`
	DocumentSHA256   string                  `json:"document_sha256"`
	CoverageRevision uint64                  `json:"coverage_revision"`
	CoveredNodes     uint64                  `json:"covered_nodes"`
	TotalNodes       uint64                  `json:"total_nodes"`
	Evidence         *AssimilationEvidence   `json:"evidence,omitempty"`
	Statements       []AssimilationStatement `json:"statements"`
}

type AssimilationEvidence struct {
	Version               string   `json:"version"`
	Author                string   `json:"author"`
	Reviewers             []string `json:"reviewers"`
	ReviewArtifactSHA256s []string `json:"review_artifact_sha256s"`
	CoverageRevision      uint64   `json:"coverage_revision"`
	CoveredNodes          uint64   `json:"covered_nodes"`
	TotalNodes            uint64   `json:"total_nodes"`
}

type AssimilationPlan struct {
	Version  string               `json:"version"`
	PlanID   string               `json:"plan_id"`
	Status   string               `json:"status"`
	Proposal AssimilationProposal `json:"proposal"`
	SHA256   string               `json:"sha256"`
}

type AssimilationStatementReceipt struct {
	Index          int      `json:"index"`
	Kind           string   `json:"kind"`
	AffectedRows   uint64   `json:"affected_rows"`
	ObjectIDs      []string `json:"object_ids,omitempty"`
	Revision       *uint64  `json:"revision,omitempty"`
	CommitSequence *uint64  `json:"commit_sequence,omitempty"`
}

type AssimilationReceipt struct {
	Version        string                         `json:"version"`
	ReceiptID      string                         `json:"receipt_id"`
	PlanID         string                         `json:"plan_id"`
	PlanSHA256     string                         `json:"plan_sha256"`
	Database       string                         `json:"database"`
	SourceID       string                         `json:"source_id"`
	SourceSHA256   string                         `json:"source_sha256"`
	DocumentSHA256 string                         `json:"document_sha256"`
	Evidence       *AssimilationEvidence          `json:"evidence,omitempty"`
	Status         string                         `json:"status"`
	Replayed       bool                           `json:"replayed"`
	Statements     []AssimilationStatementReceipt `json:"statements"`
}

func (receipt AssimilationReceipt) Validate() error {
	if receipt.Version != AssimilationReceiptVersion ||
		!validAssimilationText(receipt.ReceiptID, 200) || receipt.PlanID != receipt.ReceiptID ||
		!validAssimilationSHA256(receipt.PlanSHA256) ||
		!validAssimilationText(receipt.Database, 200) ||
		!validAssimilationText(receipt.SourceID, 200) ||
		!validAssimilationSHA256(receipt.SourceSHA256) ||
		!validAssimilationSHA256(receipt.DocumentSHA256) || receipt.Statements == nil ||
		(receipt.Status != AssimilationCommitted && receipt.Status != AssimilationInDoubt) {
		return ErrInvalidEnvelope
	}
	if receipt.Status == AssimilationCommitted && len(receipt.Statements) == 0 {
		return ErrInvalidEnvelope
	}
	for index, statement := range receipt.Statements {
		if statement.Index != index || !validAssimilationText(statement.Kind, 80) || statement.AffectedRows == 0 {
			return ErrInvalidEnvelope
		}
		if statement.ObjectIDs != nil {
			if uint64(len(statement.ObjectIDs)) != statement.AffectedRows || !validAssimilationObjectIDs(statement.ObjectIDs) {
				return ErrInvalidEnvelope
			}
		}
	}
	if receipt.Evidence != nil && receipt.Evidence.Validate() != nil {
		return ErrInvalidEnvelope
	}
	return nil
}

func SealAssimilationPlan(proposal AssimilationProposal) (AssimilationPlan, error) {
	if err := proposal.Validate(); err != nil {
		return AssimilationPlan{}, err
	}
	plan := AssimilationPlan{
		Version: AssimilationPlanVersion, PlanID: proposal.ProposalID,
		Status: AssimilationPlanReview, Proposal: cloneAssimilationProposal(proposal),
	}
	digest, err := assimilationPlanDigest(plan)
	if err != nil {
		return AssimilationPlan{}, ErrInvalidRequest
	}
	plan.SHA256 = digest
	return cloneAssimilationPlan(plan), nil
}

func (proposal AssimilationProposal) Validate() error {
	if proposal.Version != AssimilationProposalVersion ||
		!validAssimilationText(proposal.ProposalID, 200) ||
		!validAssimilationText(proposal.Database, 200) ||
		!validAssimilationText(proposal.Author, 160) ||
		!validAssimilationText(proposal.SourceID, 200) ||
		!validAssimilationText(proposal.SourceLocator, 1000) ||
		!validAssimilationSHA256(proposal.SourceSHA256) ||
		!validAssimilationSHA256(proposal.DocumentSHA256) ||
		proposal.CoverageRevision == 0 || proposal.TotalNodes == 0 ||
		proposal.CoveredNodes != proposal.TotalNodes ||
		len(proposal.Statements) == 0 || len(proposal.Statements) > MaxAssimilationStatements {
		return ErrInvalidRequest
	}
	if proposal.Evidence != nil && (proposal.Evidence.Validate() != nil ||
		proposal.Evidence.Author != proposal.Author ||
		proposal.Evidence.CoverageRevision != proposal.CoverageRevision ||
		proposal.Evidence.CoveredNodes != proposal.CoveredNodes ||
		proposal.Evidence.TotalNodes != proposal.TotalNodes) {
		return ErrInvalidRequest
	}
	for _, statement := range proposal.Statements {
		mutation := statement.Mutation
		if strings.TrimSpace(statement.MSQL) == "" || len(statement.MSQL) > 64<<10 ||
			mutation.ExpectedSchemaVersion == 0 || mutation.MaxAffectedRows == 0 ||
			mutation.MaxAffectedRows > 64 || mutation.Actor != proposal.Author ||
			strings.TrimSpace(mutation.Source) == "" || strings.TrimSpace(mutation.Reason) == "" ||
			mutation.SourceKind != SourceDocumentAnchor || mutation.SourceReceiptID != "" ||
			mutation.SourceLocator != proposal.SourceLocator || mutation.SourceContentHash != proposal.SourceSHA256 {
			return ErrInvalidRequest
		}
	}
	encoded, err := json.Marshal(proposal)
	if err != nil || len(encoded) > MaxAssimilationPlanBytes {
		return ErrInvalidRequest
	}
	return nil
}

func (plan AssimilationPlan) Validate() error {
	if plan.Version != AssimilationPlanVersion || plan.PlanID != plan.Proposal.ProposalID ||
		plan.Status != AssimilationPlanReview || !validAssimilationSHA256(plan.SHA256) ||
		plan.Proposal.Validate() != nil {
		return ErrInvalidRequest
	}
	digest, err := assimilationPlanDigest(plan)
	if err != nil || digest != plan.SHA256 {
		return ErrInvalidRequest
	}
	return nil
}

func cloneAssimilationPlan(plan AssimilationPlan) AssimilationPlan {
	plan.Proposal = cloneAssimilationProposal(plan.Proposal)
	return plan
}

func cloneAssimilationProposal(proposal AssimilationProposal) AssimilationProposal {
	proposal.Evidence = cloneAssimilationEvidence(proposal.Evidence)
	proposal.Statements = append([]AssimilationStatement(nil), proposal.Statements...)
	for index := range proposal.Statements {
		proposal.Statements[index].Parameters.Named = cloneAssimilationValues(proposal.Statements[index].Parameters.Named)
		proposal.Statements[index].Parameters.Positional = cloneAssimilationSlice(proposal.Statements[index].Parameters.Positional)
		mutation := proposal.Statements[index].Mutation
		mutation.SourceRevisions = cloneAssimilationMap(mutation.SourceRevisions)
		mutation.RouteLeafIDs = append([]string(nil), mutation.RouteLeafIDs...)
		mutation.TargetRouteLeafIDs = cloneAssimilationStrings(mutation.TargetRouteLeafIDs)
		mutation.RelationTargetOrdinals = cloneAssimilationMap(mutation.RelationTargetOrdinals)
		mutation.RouteUpdates = append([]RouteUpdate(nil), mutation.RouteUpdates...)
		proposal.Statements[index].Mutation = mutation
	}
	return proposal
}

func (evidence AssimilationEvidence) Validate() error {
	if evidence.Version != AssimilationEvidenceVersion ||
		!validAssimilationText(evidence.Author, 160) || evidence.Reviewers == nil ||
		len(evidence.Reviewers) == 0 || len(evidence.Reviewers) > MaxAssimilationStatements ||
		len(evidence.ReviewArtifactSHA256s) != len(evidence.Reviewers) ||
		evidence.CoverageRevision == 0 || evidence.TotalNodes == 0 ||
		evidence.CoveredNodes != evidence.TotalNodes {
		return ErrInvalidRequest
	}
	seenArtifacts := make(map[string]struct{}, len(evidence.ReviewArtifactSHA256s))
	for index, reviewer := range evidence.Reviewers {
		artifact := evidence.ReviewArtifactSHA256s[index]
		if !validAssimilationText(reviewer, 160) || !validAssimilationSHA256(artifact) {
			return ErrInvalidRequest
		}
		if _, duplicate := seenArtifacts[artifact]; duplicate {
			return ErrInvalidRequest
		}
		seenArtifacts[artifact] = struct{}{}
	}
	return nil
}

func cloneAssimilationEvidence(evidence *AssimilationEvidence) *AssimilationEvidence {
	if evidence == nil {
		return nil
	}
	cloned := *evidence
	cloned.Reviewers = append([]string(nil), evidence.Reviewers...)
	cloned.ReviewArtifactSHA256s = append([]string(nil), evidence.ReviewArtifactSHA256s...)
	return &cloned
}

func validAssimilationObjectIDs(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validAssimilationText(value, 200) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func assimilationPlanDigest(plan AssimilationPlan) (string, error) {
	plan.SHA256 = ""
	encoded, err := json.Marshal(plan)
	if err != nil || len(encoded) > MaxAssimilationPlanBytes {
		return "", ErrInvalidRequest
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validAssimilationText(value string, limit int) bool {
	value = strings.TrimSpace(value)
	return value != "" && utf8.ValidString(value) && len(value) <= limit &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func validAssimilationSHA256(value string) bool {
	value = strings.TrimPrefix(value, "sha256:")
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func cloneAssimilationMap[K comparable, V any](source map[K]V) map[K]V {
	if source == nil {
		return nil
	}
	value := make(map[K]V, len(source))
	for key, item := range source {
		value[key] = item
	}
	return value
}

func cloneAssimilationSlice(source []any) []any {
	if source == nil {
		return nil
	}
	value := make([]any, len(source))
	for index := range source {
		value[index] = cloneAssimilationAny(source[index])
	}
	return value
}

func cloneAssimilationValues(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	value := make(map[string]any, len(source))
	for key, item := range source {
		value[key] = cloneAssimilationAny(item)
	}
	return value
}

func cloneAssimilationAny(source any) any {
	switch value := source.(type) {
	case map[string]any:
		return cloneAssimilationValues(value)
	case []any:
		return cloneAssimilationSlice(value)
	case []string:
		return append([]string(nil), value...)
	case map[string]string:
		return cloneAssimilationMap(value)
	case map[string]uint64:
		return cloneAssimilationMap(value)
	default:
		return value
	}
}

func cloneAssimilationStrings(source [][]string) [][]string {
	if source == nil {
		return nil
	}
	value := make([][]string, len(source))
	for index := range source {
		value[index] = append([]string(nil), source[index]...)
	}
	return value
}
