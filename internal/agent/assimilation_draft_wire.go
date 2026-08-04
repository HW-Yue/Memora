package agent

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

type assimilationDraftContext struct {
	Version   string         `json:"version"`
	JobID     string         `json:"job_id"`
	Database  string         `json:"database"`
	Extent    ReadExtent     `json:"extent"`
	Bootstrap BootstrapFrame `json:"bootstrap"`
}

type assimilationDraftToolArguments struct {
	Claims []assimilationDraftClaimArgument `json:"claims"`
}

type assimilationDraftClaimArgument struct {
	ClaimID                string                     `json:"claim_id"`
	SourceNodeIDs          []string                   `json:"source_node_ids"`
	MSQL                   string                     `json:"msql"`
	Named                  map[string]any             `json:"named"`
	Positional             []any                      `json:"positional,omitempty"`
	ExpectedSchemaVersion  uint64                     `json:"expected_schema_version"`
	ExpectedRevision       uint64                     `json:"expected_revision,omitempty"`
	SourceRevisions        map[string]uint64          `json:"source_revisions,omitempty"`
	MaxAffectedRows        uint64                     `json:"max_affected_rows"`
	RouteLeafIDs           []string                   `json:"route_leaf_ids,omitempty"`
	TargetRouteLeafIDs     [][]string                 `json:"target_route_leaf_ids,omitempty"`
	RelationTargetOrdinals map[string]int             `json:"relation_target_ordinals,omitempty"`
	RouteUpdates           []protocolmsql.RouteUpdate `json:"route_updates,omitempty"`
	Reason                 string                     `json:"reason"`
}

var assimilationDraftToolSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["claims"],"properties":{"claims":{"type":"array","maxItems":32,"items":{"type":"object","additionalProperties":false,"required":["claim_id","source_node_ids","msql","named","expected_schema_version","max_affected_rows","reason"],"properties":{"claim_id":{"type":"string","minLength":1,"maxLength":160},"source_node_ids":{"type":"array","minItems":1,"maxItems":32,"items":{"type":"string","minLength":1,"maxLength":160}},"msql":{"type":"string","minLength":1,"maxLength":65536},"named":{"type":"object"},"positional":{"type":"array"},"expected_schema_version":{"type":"integer","minimum":1},"expected_revision":{"type":"integer","minimum":0},"source_revisions":{"type":"object","additionalProperties":{"type":"integer","minimum":1}},"max_affected_rows":{"type":"integer","minimum":1},"route_leaf_ids":{"type":"array","maxItems":32,"items":{"type":"string","minLength":1,"maxLength":160}},"target_route_leaf_ids":{"type":"array","maxItems":32,"items":{"type":"array","maxItems":32,"items":{"type":"string","minLength":1,"maxLength":160}}},"relation_target_ordinals":{"type":"object","additionalProperties":{"type":"integer","minimum":0}},"route_updates":{"type":"array","maxItems":32,"items":{"type":"object","additionalProperties":false,"required":["route_id","expected_revision"],"properties":{"route_id":{"type":"string","minLength":1,"maxLength":160},"expected_revision":{"type":"integer","minimum":1},"purpose":{"type":"string","maxLength":1000},"synopsis":{"type":"string","maxLength":2000}}}},"reason":{"type":"string","minLength":1,"maxLength":1000}}}}}}`)

func (drafter *AssimilationDrafter) providerRequest(
	request AssimilationDraftRequest,
	frame BootstrapFrame,
) (ProviderRequest, error) {
	content, err := json.Marshal(assimilationDraftContext{
		Version: AssimilationDraftContextVersion, JobID: request.JobID,
		Database: request.Database, Extent: cloneReadExtent(request.Extent), Bootstrap: frame,
	})
	if err != nil || len(content) > 1<<20 {
		return ProviderRequest{}, ErrInvalidAssimilationDraftRequest
	}
	providerRequest := ProviderRequest{
		Version: ProviderRequestVersion, Model: drafter.model,
		Messages: []ProviderMessage{
			{Role: ProviderRoleSystem, Content: AssimilationDraftSystemPrompt},
			{Role: ProviderRoleUser, Content: string(content)},
		},
		Tools: []ProviderTool{{
			Name:        AssimilationDraftToolName,
			Description: "Return zero or more source-anchored semantic claims with candidate parameterized MSQL mutations.",
			InputSchema: append(json.RawMessage(nil), assimilationDraftToolSchema...),
		}},
		ToolChoice: ProviderToolChoiceRequired, MaxOutputTokens: drafter.maxOutputTokens,
	}
	if providerRequest.Validate() != nil {
		return ProviderRequest{}, ErrInvalidAssimilationDraftRequest
	}
	return providerRequest, nil
}

func (drafter *AssimilationDrafter) decodeToolResponse(
	response ProviderResponse,
	extent ReadExtent,
) (assimilationDraftToolArguments, error) {
	if response.FinishReason != ProviderFinishToolCalls || len(response.Message.ToolCalls) != 1 ||
		response.Message.ToolCalls[0].Name != AssimilationDraftToolName {
		return assimilationDraftToolArguments{}, ErrInvalidAssimilationDraftToolCall
	}
	raw := response.Message.ToolCalls[0].Arguments
	if len(raw) == 0 || len(raw) > maxAssimilationDraftToolBytes {
		return assimilationDraftToolArguments{}, ErrInvalidAssimilationDraftToolCall
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var arguments assimilationDraftToolArguments
	if err := decoder.Decode(&arguments); err != nil || requireJSONEOF(decoder) != nil ||
		arguments.Claims == nil || uint64(len(arguments.Claims)) > drafter.maxClaims {
		return assimilationDraftToolArguments{}, ErrInvalidAssimilationDraftToolCall
	}
	nodes := make(map[string]struct{}, len(extent.Nodes))
	for _, node := range extent.Nodes {
		nodes[node.NodeID] = struct{}{}
	}
	claimIDs := make(map[string]struct{}, len(arguments.Claims))
	for index := range arguments.Claims {
		claim := &arguments.Claims[index]
		if !validSourceIntakeID(claim.ClaimID, 160) || len(claim.SourceNodeIDs) == 0 ||
			len(claim.SourceNodeIDs) > 32 || strings.TrimSpace(claim.MSQL) == "" ||
			!utf8.ValidString(claim.MSQL) || len(claim.MSQL) > 64<<10 || strings.ContainsRune(claim.MSQL, '\x00') ||
			claim.ExpectedSchemaVersion == 0 || claim.MaxAffectedRows == 0 ||
			claim.MaxAffectedRows > drafter.profile.MaxAffectedRows || !validWriteText(claim.Reason, 1000) {
			return assimilationDraftToolArguments{}, ErrInvalidAssimilationDraftToolCall
		}
		if _, duplicate := claimIDs[claim.ClaimID]; duplicate {
			return assimilationDraftToolArguments{}, ErrInvalidAssimilationDraftToolCall
		}
		claimIDs[claim.ClaimID] = struct{}{}
		seenNodes := make(map[string]struct{}, len(claim.SourceNodeIDs))
		for _, nodeID := range claim.SourceNodeIDs {
			if _, found := nodes[nodeID]; !found {
				return assimilationDraftToolArguments{}, ErrInvalidAssimilationDraftToolCall
			}
			if _, duplicate := seenNodes[nodeID]; duplicate {
				return assimilationDraftToolArguments{}, ErrInvalidAssimilationDraftToolCall
			}
			seenNodes[nodeID] = struct{}{}
		}
		named, ok := normalizeQueryJSONObject(claim.Named, 0)
		if !ok {
			return assimilationDraftToolArguments{}, ErrInvalidAssimilationDraftToolCall
		}
		positional, ok := normalizeQueryJSONArray(claim.Positional, 0)
		if !ok || !validAssimilationDraftGuards(*claim) {
			return assimilationDraftToolArguments{}, ErrInvalidAssimilationDraftToolCall
		}
		claim.Named, claim.Positional = named, positional
		claim.SourceNodeIDs = append([]string(nil), claim.SourceNodeIDs...)
		claim.SourceRevisions = cloneAssimilationDraftMap(claim.SourceRevisions)
		claim.RouteLeafIDs = append([]string(nil), claim.RouteLeafIDs...)
		claim.TargetRouteLeafIDs = cloneAssimilationDraftNestedStrings(claim.TargetRouteLeafIDs)
		claim.RelationTargetOrdinals = cloneAssimilationDraftMap(claim.RelationTargetOrdinals)
		claim.RouteUpdates = append([]protocolmsql.RouteUpdate(nil), claim.RouteUpdates...)
	}
	return arguments, nil
}

func validAssimilationDraftGuards(claim assimilationDraftClaimArgument) bool {
	if len(claim.SourceRevisions) > 64 || len(claim.RouteLeafIDs) > 32 ||
		len(claim.TargetRouteLeafIDs) > 32 || len(claim.RelationTargetOrdinals) > 64 ||
		len(claim.RouteUpdates) > 32 {
		return false
	}
	for key, revision := range claim.SourceRevisions {
		if !validWriteText(key, 160) || revision == 0 {
			return false
		}
	}
	if !validAssimilationDraftIdentifiers(claim.RouteLeafIDs) {
		return false
	}
	for _, identifiers := range claim.TargetRouteLeafIDs {
		if len(identifiers) > 32 || !validAssimilationDraftIdentifiers(identifiers) {
			return false
		}
	}
	for key, ordinal := range claim.RelationTargetOrdinals {
		if !validWriteText(key, 160) || ordinal < 0 {
			return false
		}
	}
	for _, update := range claim.RouteUpdates {
		if !validWriteText(update.RouteID, 160) || update.ExpectedRevision == 0 ||
			(update.Purpose != "" && !validWriteText(update.Purpose, 1000)) ||
			(update.Synopsis != nil && *update.Synopsis != "" && !validWriteText(*update.Synopsis, 2000)) {
			return false
		}
	}
	return true
}

func validAssimilationDraftIdentifiers(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validWriteText(value, 160) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func cloneAssimilationDraftMap[K comparable, V any](source map[K]V) map[K]V {
	if source == nil {
		return nil
	}
	cloned := make(map[K]V, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneAssimilationDraftNestedStrings(source [][]string) [][]string {
	if source == nil {
		return nil
	}
	cloned := make([][]string, len(source))
	for index := range source {
		cloned[index] = append([]string(nil), source[index]...)
	}
	return cloned
}
