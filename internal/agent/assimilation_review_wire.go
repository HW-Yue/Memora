package agent

import (
	"bytes"
	"encoding/json"
	"slices"
)

const maxAssimilationReviewToolBytes = 1 << 20

type assimilationReviewContext struct {
	Version         string                                   `json:"version"`
	Reviewer        string                                   `json:"reviewer"`
	Challenge       string                                   `json:"challenge"`
	Batch           DraftClaimBatch                          `json:"batch"`
	Extent          ReadExtent                               `json:"extent"`
	PeerClaims      []DraftClaim                             `json:"peer_claims"`
	NumericEvidence map[string][]assimilationNumericEvidence `json:"numeric_evidence"`
}

type assimilationReviewToolArguments struct {
	Challenge string                        `json:"challenge"`
	Checks    []assimilationReviewToolCheck `json:"checks"`
}

type assimilationReviewToolCheck struct {
	ClaimID            string   `json:"claim_id"`
	SourceNodeIDs      []string `json:"source_node_ids"`
	NumericEvidenceIDs []string `json:"numeric_evidence_ids"`
	AnchorVerified     bool     `json:"anchor_verified"`
	NumbersVerified    bool     `json:"numbers_verified"`
	ConflictFree       bool     `json:"conflict_free"`
	NotRawCopy         bool     `json:"not_raw_copy"`
	Decision           string   `json:"decision"`
	FindingCodes       []string `json:"finding_codes"`
}

var assimilationReviewToolSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["challenge","checks"],"properties":{"challenge":{"type":"string","minLength":64,"maxLength":64},"checks":{"type":"array","minItems":1,"maxItems":32,"items":{"type":"object","additionalProperties":false,"required":["claim_id","source_node_ids","numeric_evidence_ids","anchor_verified","numbers_verified","conflict_free","not_raw_copy","decision","finding_codes"],"properties":{"claim_id":{"type":"string","minLength":1,"maxLength":160},"source_node_ids":{"type":"array","minItems":1,"maxItems":32,"items":{"type":"string","minLength":1,"maxLength":160}},"numeric_evidence_ids":{"type":"array","maxItems":256,"items":{"type":"string","minLength":71,"maxLength":71}},"anchor_verified":{"type":"boolean"},"numbers_verified":{"type":"boolean"},"conflict_free":{"type":"boolean"},"not_raw_copy":{"type":"boolean"},"decision":{"type":"string","enum":["accept","reject","needs_user"]},"finding_codes":{"type":"array","maxItems":16,"items":{"type":"string","minLength":1,"maxLength":80}}}}}}}`)

func (reviewer *AssimilationReviewer) providerRequest(
	request AssimilationReviewRequest,
	inventories map[string][]assimilationNumericEvidence,
	challenge string,
) (ProviderRequest, error) {
	content, err := json.Marshal(assimilationReviewContext{
		Version: AssimilationReviewContextVersion, Reviewer: reviewer.reviewer,
		Challenge: challenge, Batch: cloneDraftClaimBatch(request.Batch),
		Extent: cloneReadExtent(request.Extent), PeerClaims: cloneDraftClaims(request.PeerClaims),
		NumericEvidence: cloneAssimilationNumericEvidence(inventories),
	})
	if err != nil || len(content) > maxAssimilationReviewToolBytes {
		return ProviderRequest{}, ErrInvalidAssimilationReview
	}
	providerRequest := ProviderRequest{
		Version: ProviderRequestVersion, Model: reviewer.model,
		Messages: []ProviderMessage{
			{Role: ProviderRoleSystem, Content: AssimilationReviewSystemPrompt},
			{Role: ProviderRoleUser, Content: string(content)},
		},
		Tools: []ProviderTool{{
			Name:        AssimilationReviewToolName,
			Description: "Return one challenge-bound independent semantic review check for every target claim.",
			InputSchema: append(json.RawMessage(nil), assimilationReviewToolSchema...),
		}},
		ToolChoice: ProviderToolChoiceRequired, MaxOutputTokens: reviewer.maxOutputTokens,
	}
	if providerRequest.Validate() != nil {
		return ProviderRequest{}, ErrInvalidAssimilationReview
	}
	return providerRequest, nil
}

func (reviewer *AssimilationReviewer) decodeReview(
	response ProviderResponse,
	batch DraftClaimBatch,
	inventories map[string][]assimilationNumericEvidence,
	challenge string,
) ([]AssimilationReviewCheck, error) {
	if response.FinishReason != ProviderFinishToolCalls || len(response.Message.ToolCalls) != 1 ||
		response.Message.ToolCalls[0].Name != AssimilationReviewToolName {
		return nil, ErrInvalidAssimilationReview
	}
	raw := response.Message.ToolCalls[0].Arguments
	if len(raw) == 0 || len(raw) > maxAssimilationReviewToolBytes {
		return nil, ErrInvalidAssimilationReview
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var arguments assimilationReviewToolArguments
	if err := decoder.Decode(&arguments); err != nil || requireJSONEOF(decoder) != nil ||
		arguments.Challenge != challenge || len(arguments.Checks) != len(batch.Claims) {
		return nil, ErrInvalidAssimilationReview
	}
	checks := make([]AssimilationReviewCheck, len(arguments.Checks))
	for index, argument := range arguments.Checks {
		claim := batch.Claims[index]
		if argument.ClaimID != claim.ClaimID || argument.SourceNodeIDs == nil ||
			argument.NumericEvidenceIDs == nil || argument.FindingCodes == nil ||
			!validAssimilationReviewDecision(argument.Decision) {
			return nil, ErrInvalidAssimilationReview
		}
		wantNodes := make([]string, len(claim.Sources))
		for sourceIndex, source := range claim.Sources {
			wantNodes[sourceIndex] = source.NodeID
		}
		wantNumeric := make([]string, len(inventories[claim.ClaimID]))
		for evidenceIndex, evidence := range inventories[claim.ClaimID] {
			wantNumeric[evidenceIndex] = evidence.ID
		}
		if !slices.Equal(argument.SourceNodeIDs, wantNodes) ||
			!slices.Equal(argument.NumericEvidenceIDs, wantNumeric) ||
			!validAssimilationReviewFindingCodes(argument.FindingCodes) {
			return nil, ErrInvalidAssimilationReview
		}
		checks[index] = AssimilationReviewCheck{
			Version: AssimilationReviewCheckVersion, ClaimID: claim.ClaimID, ClaimSHA256: claim.SHA256,
			SourceNodeIDs:      cloneAssimilationReviewStrings(argument.SourceNodeIDs),
			NumericEvidenceIDs: cloneAssimilationReviewStrings(argument.NumericEvidenceIDs),
			AnchorVerified:     argument.AnchorVerified, NumbersVerified: argument.NumbersVerified,
			ConflictFree: argument.ConflictFree, NotRawCopy: argument.NotRawCopy,
			Decision: argument.Decision, FindingCodes: cloneAssimilationReviewStrings(argument.FindingCodes),
		}
		if checks[index].Validate() != nil {
			return nil, ErrInvalidAssimilationReview
		}
	}
	return checks, nil
}

func validAssimilationReviewFindingCodes(values []string) bool {
	if len(values) > 16 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validSourceIntakeID(value, 80) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func cloneAssimilationNumericEvidence(
	values map[string][]assimilationNumericEvidence,
) map[string][]assimilationNumericEvidence {
	cloned := make(map[string][]assimilationNumericEvidence, len(values))
	for claimID, evidence := range values {
		cloned[claimID] = append(make([]assimilationNumericEvidence, 0, len(evidence)), evidence...)
	}
	return cloned
}
