package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	ProviderRequestVersion  = "memora.agent-provider-request/v1"
	ProviderResponseVersion = "memora.agent-provider-response/v1"
)

var (
	ErrInvalidProviderDependencies = errors.New("Provider dependencies are invalid")
	ErrInvalidProviderRequest      = errors.New("Provider request is invalid")
	ErrInvalidProviderResponse     = errors.New("Provider response is invalid")
)

type ProviderRole string

const (
	ProviderRoleSystem    ProviderRole = "system"
	ProviderRoleUser      ProviderRole = "user"
	ProviderRoleAssistant ProviderRole = "assistant"
	ProviderRoleTool      ProviderRole = "tool"
)

type ProviderToolChoice string

const (
	ProviderToolChoiceAuto     ProviderToolChoice = "auto"
	ProviderToolChoiceRequired ProviderToolChoice = "required"
	ProviderToolChoiceNone     ProviderToolChoice = "none"
)

type ProviderFinishReason string

const (
	ProviderFinishStop      ProviderFinishReason = "stop"
	ProviderFinishToolCalls ProviderFinishReason = "tool_calls"
	ProviderFinishLength    ProviderFinishReason = "length"
)

type ProviderToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ProviderMessage struct {
	Role       ProviderRole       `json:"role"`
	Content    string             `json:"content,omitempty"`
	ToolCalls  []ProviderToolCall `json:"tool_calls,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
}

type ProviderTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type ProviderRequest struct {
	Version         string             `json:"version"`
	Model           string             `json:"model"`
	Messages        []ProviderMessage  `json:"messages"`
	Tools           []ProviderTool     `json:"tools"`
	ToolChoice      ProviderToolChoice `json:"tool_choice"`
	MaxOutputTokens uint64             `json:"max_output_tokens"`
}

func (request ProviderRequest) Validate() error {
	if request.Version != ProviderRequestVersion || invalidProviderText(request.Model, 200, true) ||
		len(request.Messages) == 0 || len(request.Messages) > 128 || len(request.Tools) > 32 ||
		request.MaxOutputTokens < 1 || request.MaxOutputTokens > 65536 ||
		(request.ToolChoice != ProviderToolChoiceAuto && request.ToolChoice != ProviderToolChoiceRequired &&
			request.ToolChoice != ProviderToolChoiceNone) {
		return ErrInvalidProviderRequest
	}
	tools := make(map[string]bool, len(request.Tools))
	for _, tool := range request.Tools {
		if !validProviderName(tool.Name) || tools[tool.Name] || invalidProviderText(tool.Description, 1024, true) ||
			!validProviderJSONObject(tool.InputSchema, 16384) {
			return ErrInvalidProviderRequest
		}
		tools[tool.Name] = true
	}
	if request.ToolChoice == ProviderToolChoiceRequired && len(tools) == 0 {
		return ErrInvalidProviderRequest
	}
	calls := make(map[string]string)
	answered := make(map[string]bool)
	for _, message := range request.Messages {
		if err := validateProviderMessage(message, tools, calls, answered, false); err != nil {
			return ErrInvalidProviderRequest
		}
	}
	return nil
}

type ProviderUsage struct {
	InputTokens       uint64 `json:"input_tokens"`
	CachedInputTokens uint64 `json:"cached_input_tokens,omitempty"`
	OutputTokens      uint64 `json:"output_tokens"`
	TotalTokens       uint64 `json:"total_tokens"`
}

type ProviderResponse struct {
	Version      string               `json:"version"`
	Model        string               `json:"model"`
	Message      ProviderMessage      `json:"message"`
	FinishReason ProviderFinishReason `json:"finish_reason"`
	Usage        ProviderUsage        `json:"usage"`
}

func (response ProviderResponse) Validate() error {
	if response.Version != ProviderResponseVersion || invalidProviderText(response.Model, 200, true) ||
		(response.FinishReason != ProviderFinishStop && response.FinishReason != ProviderFinishToolCalls &&
			response.FinishReason != ProviderFinishLength) ||
		response.Usage.CachedInputTokens > response.Usage.InputTokens ||
		response.Usage.InputTokens > ^uint64(0)-response.Usage.OutputTokens ||
		response.Usage.TotalTokens != response.Usage.InputTokens+response.Usage.OutputTokens {
		return ErrInvalidProviderResponse
	}
	if err := validateProviderMessage(response.Message, nil, make(map[string]string), make(map[string]bool), true); err != nil {
		return ErrInvalidProviderResponse
	}
	if response.FinishReason == ProviderFinishToolCalls && len(response.Message.ToolCalls) == 0 {
		return ErrInvalidProviderResponse
	}
	if response.FinishReason != ProviderFinishToolCalls && len(response.Message.ToolCalls) != 0 {
		return ErrInvalidProviderResponse
	}
	return nil
}

// Provider is the vendor-neutral, non-streaming model completion port.
type Provider interface {
	Complete(context.Context, ProviderRequest) (ProviderResponse, error)
}

type ProviderGateway struct {
	provider Provider
}

func NewProviderGateway(provider Provider) (*ProviderGateway, error) {
	if provider == nil {
		return nil, ErrInvalidProviderDependencies
	}
	return &ProviderGateway{provider: provider}, nil
}

// Complete validates both sides, calls the Provider exactly once, and owns no
// retry or fallback policy.
func (gateway *ProviderGateway) Complete(
	ctx context.Context,
	request ProviderRequest,
) (ProviderResponse, error) {
	if ctx == nil {
		return ProviderResponse{}, ErrInvalidProviderRequest
	}
	if err := request.Validate(); err != nil {
		return ProviderResponse{}, err
	}
	response, err := gateway.provider.Complete(ctx, request)
	if err != nil {
		return ProviderResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return ProviderResponse{}, err
	}
	if response.Model != request.Model {
		return ProviderResponse{}, fmt.Errorf("%w: response model differs from request", ErrInvalidProviderResponse)
	}
	return response, nil
}

func validateProviderMessage(
	message ProviderMessage,
	tools map[string]bool,
	calls map[string]string,
	answered map[string]bool,
	response bool,
) error {
	if len(message.Content) > 1<<20 || strings.ContainsRune(message.Content, '\x00') || len(message.ToolCalls) > 32 {
		return errors.New("message budget exceeded")
	}
	switch message.Role {
	case ProviderRoleSystem, ProviderRoleUser:
		if response || strings.TrimSpace(message.Content) == "" || len(message.ToolCalls) != 0 || message.ToolCallID != "" {
			return errors.New("invalid input message")
		}
	case ProviderRoleAssistant:
		if message.ToolCallID != "" || (strings.TrimSpace(message.Content) == "" && len(message.ToolCalls) == 0) {
			return errors.New("invalid assistant message")
		}
		for _, call := range message.ToolCalls {
			if !validProviderCallID(call.ID) || !validProviderName(call.Name) || calls[call.ID] != "" ||
				!validProviderJSONObject(call.Arguments, 1<<20) || (!response && !tools[call.Name]) {
				return errors.New("invalid tool call")
			}
			calls[call.ID] = call.Name
		}
	case ProviderRoleTool:
		if response || strings.TrimSpace(message.Content) == "" || len(message.ToolCalls) != 0 ||
			!validProviderCallID(message.ToolCallID) || calls[message.ToolCallID] == "" || answered[message.ToolCallID] {
			return errors.New("invalid tool result")
		}
		answered[message.ToolCallID] = true
	default:
		return errors.New("unsupported role")
	}
	return nil
}

func validProviderCallID(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func validProviderJSONObject(value json.RawMessage, limit int) bool {
	if len(value) == 0 || len(value) > limit || !json.Valid(value) {
		return false
	}
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}

func validProviderName(value string) bool {
	if len(value) == 0 || len(value) > 80 {
		return false
	}
	for _, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func invalidProviderText(value string, limit int, required bool) bool {
	value = strings.TrimSpace(value)
	if required && value == "" || len(value) > limit || strings.ContainsRune(value, '\x00') {
		return true
	}
	return false
}
