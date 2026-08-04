package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

type shortTextContext struct {
	Version   string         `json:"version"`
	Intent    string         `json:"intent"`
	Title     string         `json:"title"`
	Content   string         `json:"content"`
	Locator   string         `json:"source_locator"`
	Bootstrap BootstrapFrame `json:"bootstrap"`
}

var shortTextToolSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["proposal_id","database","table","source","named","expected_schema_version","route_leaf_ids","reason"],"properties":{"proposal_id":{"type":"string","minLength":1,"maxLength":120},"database":{"type":"string","minLength":1,"maxLength":200},"table":{"type":"string","minLength":1,"maxLength":200},"source":{"type":"string","minLength":1,"maxLength":1048576},"named":{"type":"object"},"positional":{"type":"array"},"expected_schema_version":{"type":"integer","minimum":1},"route_leaf_ids":{"type":"array","minItems":1,"maxItems":8,"items":{"type":"string","minLength":1,"maxLength":160}},"reason":{"type":"string","minLength":1,"maxLength":1000}}}`)

func (agent *ShortTextAgent) providerRequest(
	request ShortTextRequest,
	frame BootstrapFrame,
) (ProviderRequest, error) {
	content, err := json.Marshal(shortTextContext{
		Version: ShortTextContextVersion, Intent: request.Intent, Title: request.Title,
		Content: request.Content, Locator: request.SourceLocator, Bootstrap: frame,
	})
	if err != nil {
		return ProviderRequest{}, ErrInvalidShortTextRequest
	}
	providerRequest := ProviderRequest{
		Version: ProviderRequestVersion, Model: agent.model,
		Messages: []ProviderMessage{
			{Role: ProviderRoleSystem, Content: ShortTextSystemPrompt},
			{Role: ProviderRoleUser, Content: string(content)},
		},
		Tools: []ProviderTool{{
			Name:        ShortTextProposeToolName,
			Description: "Propose one parameterized INSERT of one complete semantic module into an existing Table and Route Leaf.",
			InputSchema: append(json.RawMessage(nil), shortTextToolSchema...),
		}},
		ToolChoice: ProviderToolChoiceRequired, MaxOutputTokens: agent.maxOutputTokens,
	}
	if providerRequest.Validate() != nil {
		return ProviderRequest{}, ErrInvalidShortTextRequest
	}
	return providerRequest, nil
}

type shortTextToolArguments struct {
	ProposalID            string         `json:"proposal_id"`
	Database              string         `json:"database"`
	Table                 string         `json:"table"`
	Source                string         `json:"source"`
	Named                 map[string]any `json:"named"`
	Positional            []any          `json:"positional,omitempty"`
	ExpectedSchemaVersion uint64         `json:"expected_schema_version"`
	RouteLeafIDs          []string       `json:"route_leaf_ids"`
	Reason                string         `json:"reason"`
}

func (agent *ShortTextAgent) decodeToolResponse(response ProviderResponse) (shortTextToolArguments, error) {
	if response.FinishReason != ProviderFinishToolCalls || len(response.Message.ToolCalls) != 1 ||
		response.Message.ToolCalls[0].Name != ShortTextProposeToolName {
		return shortTextToolArguments{}, ErrInvalidShortTextToolCall
	}
	raw := response.Message.ToolCalls[0].Arguments
	if len(raw) == 0 || len(raw) > shortTextToolArgumentsMax {
		return shortTextToolArguments{}, ErrInvalidShortTextToolCall
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var arguments shortTextToolArguments
	if err := decoder.Decode(&arguments); err != nil {
		return shortTextToolArguments{}, ErrInvalidShortTextToolCall
	}
	if err := shortTextJSONEOF(decoder); err != nil || !validTraceLabel(arguments.ProposalID, 120) ||
		!validShortIdentifier(arguments.Database) || !validShortIdentifier(arguments.Table) ||
		strings.TrimSpace(arguments.Source) == "" || !utf8.ValidString(arguments.Source) ||
		len(arguments.Source) > 1<<20 || arguments.ExpectedSchemaVersion == 0 ||
		len(arguments.RouteLeafIDs) == 0 || len(arguments.RouteLeafIDs) > 8 ||
		!validWriteText(arguments.Reason, 1000) || !agent.databaseAllowed(arguments.Database) {
		return shortTextToolArguments{}, ErrInvalidShortTextToolCall
	}
	named, ok := normalizeQueryJSONObject(arguments.Named, 0)
	if !ok {
		return shortTextToolArguments{}, ErrInvalidShortTextToolCall
	}
	positional, ok := normalizeQueryJSONArray(arguments.Positional, 0)
	if !ok {
		return shortTextToolArguments{}, ErrInvalidShortTextToolCall
	}
	seenLeaves := make(map[string]bool, len(arguments.RouteLeafIDs))
	for _, leafID := range arguments.RouteLeafIDs {
		if !validWriteText(leafID, 160) || seenLeaves[leafID] {
			return shortTextToolArguments{}, ErrInvalidShortTextToolCall
		}
		seenLeaves[leafID] = true
	}
	arguments.Named, arguments.Positional = named, positional
	arguments.RouteLeafIDs = append([]string(nil), arguments.RouteLeafIDs...)
	return arguments, nil
}

func shortTextJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("trailing JSON value")
	}
	return err
}
