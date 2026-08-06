package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

var queryMSQLToolSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["source","statements"],"properties":{"source":{"type":"string","minLength":1,"maxLength":1048576},"statements":{"type":"array","minItems":1,"maxItems":32,"items":{"type":"object","additionalProperties":false,"properties":{"named":{"type":"object"},"positional":{"type":"array"}}}}}}`)

type queryBootstrapContext struct {
	Version   string         `json:"version"`
	Question  string         `json:"question"`
	Bootstrap BootstrapFrame `json:"bootstrap"`
}

type queryCurrentContext struct {
	Version  string `json:"version"`
	Question string `json:"question"`
}

type queryToolArguments struct {
	Source     string               `json:"source"`
	Statements []queryToolStatement `json:"statements"`
}

type queryToolStatement struct {
	Named      map[string]any `json:"named,omitempty"`
	Positional []any          `json:"positional,omitempty"`
}

func makeQueryProviderRequest(
	request QueryRequest,
	frame BootstrapFrame,
	previousCall *ProviderToolCall,
	previousResult string,
	choice ProviderToolChoice,
) (ProviderRequest, error) {
	messages := []ProviderMessage{{Role: ProviderRoleSystem, Content: QueryAgentSystemPrompt}}
	if previousCall == nil {
		content, err := json.Marshal(queryBootstrapContext{
			Version: QueryBootstrapContextVersion, Question: request.Question, Bootstrap: frame,
		})
		if err != nil {
			return ProviderRequest{}, fmt.Errorf("%w: Bootstrap context could not be encoded", ErrInvalidQueryRequest)
		}
		messages = append(messages, ProviderMessage{Role: ProviderRoleUser, Content: string(content)})
	} else {
		content, err := json.Marshal(queryCurrentContext{Version: QueryCurrentContextVersion, Question: request.Question})
		if err != nil {
			return ProviderRequest{}, fmt.Errorf("%w: current context could not be encoded", ErrInvalidQueryRequest)
		}
		call := *previousCall
		call.Arguments = append(json.RawMessage(nil), previousCall.Arguments...)
		messages = append(messages,
			ProviderMessage{Role: ProviderRoleUser, Content: string(content)},
			ProviderMessage{Role: ProviderRoleAssistant, ToolCalls: []ProviderToolCall{call}},
			ProviderMessage{Role: ProviderRoleTool, ToolCallID: call.ID, Content: previousResult},
		)
	}
	providerRequest := ProviderRequest{
		Version: ProviderRequestVersion, Model: request.Model, Messages: messages,
		Tools: []ProviderTool{{
			Name:        QueryExecuteMSQLToolName,
			Description: "Execute one complete read-only MSQL batch. Put independent reads in one batch.",
			InputSchema: append(json.RawMessage(nil), queryMSQLToolSchema...),
		}},
		ToolChoice: choice, MaxOutputTokens: request.Budget.MaxOutputTokens,
	}
	if err := providerRequest.Validate(); err != nil {
		return ProviderRequest{}, fmt.Errorf("%w: %v", ErrInvalidQueryRequest, err)
	}
	return providerRequest, nil
}

func decodeQueryToolArguments(
	raw json.RawMessage,
	authorization protocolmsql.Authorization,
	budget QueryBudget,
) (protocolmsql.Request, error) {
	if len(raw) == 0 || uint64(len(raw)) > budget.ToolArgumentsUTF8Bytes {
		return protocolmsql.Request{}, fmt.Errorf("%w: tool arguments exceeded their byte budget", ErrQueryBudget)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var arguments queryToolArguments
	if err := decoder.Decode(&arguments); err != nil {
		return protocolmsql.Request{}, fmt.Errorf("%w: arguments JSON: %v", ErrInvalidQueryToolCall, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return protocolmsql.Request{}, fmt.Errorf("%w: arguments JSON has trailing content", ErrInvalidQueryToolCall)
	}
	// Some OpenAI-compatible providers encode a single parameterless tool
	// statement as an explicit empty array. Missing statements remains nil and
	// invalid; a multi-statement source still fails Request.Validate because the
	// normalized parameter list contains only one entry.
	if arguments.Statements != nil && len(arguments.Statements) == 0 {
		arguments.Statements = []queryToolStatement{{}}
	}
	if strings.TrimSpace(arguments.Source) == "" || !utf8.ValidString(arguments.Source) || len(arguments.Source) > 1<<20 ||
		len(arguments.Statements) == 0 || uint64(len(arguments.Statements)) > budget.MaxToolStatements {
		return protocolmsql.Request{}, ErrInvalidQueryToolCall
	}
	if hasQueryTransactionControl(arguments.Source) {
		return protocolmsql.Request{}, ErrQueryTransactionControl
	}
	statements := make([]protocolmsql.StatementInput, len(arguments.Statements))
	for index, input := range arguments.Statements {
		named, ok := normalizeQueryJSONObject(input.Named, 0)
		if !ok {
			return protocolmsql.Request{}, ErrInvalidQueryToolCall
		}
		positional, ok := normalizeQueryJSONArray(input.Positional, 0)
		if !ok {
			return protocolmsql.Request{}, ErrInvalidQueryToolCall
		}
		statements[index] = protocolmsql.StatementInput{
			Parameters:    protocolmsql.Parameters{Named: named, Positional: positional},
			Authorization: authorization,
		}
	}
	request := protocolmsql.Request{
		Version: protocolmsql.RequestVersion, Source: arguments.Source, Statements: statements,
	}
	if err := request.Validate(); err != nil {
		return protocolmsql.Request{}, fmt.Errorf("%w: %v", ErrInvalidQueryToolCall, err)
	}
	return request, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
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

func normalizeQueryJSONObject(value map[string]any, depth int) (map[string]any, bool) {
	if value == nil {
		return nil, true
	}
	if depth > 16 {
		return nil, false
	}
	normalized := make(map[string]any, len(value))
	for key, item := range value {
		if strings.TrimSpace(key) == "" || len(key) > 160 {
			return nil, false
		}
		converted, ok := normalizeQueryJSONValue(item, depth+1)
		if !ok {
			return nil, false
		}
		normalized[key] = converted
	}
	return normalized, true
}

func normalizeQueryJSONArray(value []any, depth int) ([]any, bool) {
	if value == nil {
		return nil, true
	}
	if depth > 16 || len(value) > 1024 {
		return nil, false
	}
	normalized := make([]any, len(value))
	for index, item := range value {
		converted, ok := normalizeQueryJSONValue(item, depth+1)
		if !ok {
			return nil, false
		}
		normalized[index] = converted
	}
	return normalized, true
}

func normalizeQueryJSONValue(value any, depth int) (any, bool) {
	if depth > 16 {
		return nil, false
	}
	switch typed := value.(type) {
	case nil, bool, string:
		return typed, true
	case json.Number:
		if integer, err := strconv.ParseInt(string(typed), 10, 64); err == nil {
			return integer, true
		}
		if unsigned, err := strconv.ParseUint(string(typed), 10, 64); err == nil {
			return unsigned, true
		}
		floating, err := strconv.ParseFloat(string(typed), 64)
		return floating, err == nil && !math.IsInf(floating, 0) && !math.IsNaN(floating)
	case map[string]any:
		return normalizeQueryJSONObject(typed, depth)
	case []any:
		return normalizeQueryJSONArray(typed, depth)
	default:
		return nil, false
	}
}

func hasQueryTransactionControl(source string) bool {
	words := queryUnquotedWords(source)
	for index, word := range words {
		switch word {
		case "BEGIN", "COMMIT", "ROLLBACK":
			return true
		case "START":
			if index+1 < len(words) && words[index+1] == "TRANSACTION" {
				return true
			}
		}
	}
	return false
}

func queryUnquotedWords(source string) []string {
	words := make([]string, 0, 16)
	for offset := 0; offset < len(source); {
		if strings.HasPrefix(source[offset:], "--") {
			offset += 2
			for offset < len(source) && source[offset] != '\n' {
				offset++
			}
			continue
		}
		if strings.HasPrefix(source[offset:], "/*") {
			offset += 2
			if end := strings.Index(source[offset:], "*/"); end >= 0 {
				offset += end + 2
			} else {
				return words
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(source[offset:])
		if r == '\'' || r == '`' || r == '"' {
			offset = skipQueryQuoted(source, offset, r)
			continue
		}
		if r == '_' || unicode.IsLetter(r) {
			start := offset
			offset += size
			for offset < len(source) {
				next, nextSize := utf8.DecodeRuneInString(source[offset:])
				if next != '_' && !unicode.IsLetter(next) && !unicode.IsDigit(next) {
					break
				}
				offset += nextSize
			}
			words = append(words, strings.ToUpper(source[start:offset]))
			continue
		}
		offset += size
	}
	return words
}

func skipQueryQuoted(source string, offset int, delimiter rune) int {
	_, size := utf8.DecodeRuneInString(source[offset:])
	offset += size
	for offset < len(source) {
		r, currentSize := utf8.DecodeRuneInString(source[offset:])
		offset += currentSize
		if r != delimiter {
			continue
		}
		if offset < len(source) {
			next, nextSize := utf8.DecodeRuneInString(source[offset:])
			if next == delimiter {
				offset += nextSize
				continue
			}
		}
		return offset
	}
	return offset
}
