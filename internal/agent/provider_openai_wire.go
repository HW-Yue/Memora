package agent

import (
	"bytes"
	"encoding/json"
	"io"
)

type openAICompatibleRequest struct {
	Model               string                    `json:"model"`
	Messages            []openAICompatibleMessage `json:"messages"`
	Tools               []openAICompatibleTool    `json:"tools,omitempty"`
	ToolChoice          ProviderToolChoice        `json:"tool_choice"`
	MaxCompletionTokens uint64                    `json:"max_completion_tokens"`
	Stream              bool                      `json:"stream"`
}

type openAICompatibleMessage struct {
	Role       ProviderRole               `json:"role"`
	Content    *string                    `json:"content,omitempty"`
	ToolCalls  []openAICompatibleToolCall `json:"tool_calls,omitempty"`
	ToolCallID string                     `json:"tool_call_id,omitempty"`
}

type openAICompatibleToolCall struct {
	ID       string                       `json:"id"`
	Type     string                       `json:"type"`
	Function openAICompatibleFunctionCall `json:"function"`
}

type openAICompatibleFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAICompatibleTool struct {
	Type     string                       `json:"type"`
	Function openAICompatibleFunctionTool `json:"function"`
}

type openAICompatibleFunctionTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openAICompatibleResponse struct {
	Model   string                   `json:"model"`
	Choices []openAICompatibleChoice `json:"choices"`
	Usage   *openAICompatibleUsage   `json:"usage"`
}

type openAICompatibleChoice struct {
	Index        int                             `json:"index"`
	Message      openAICompatibleResponseMessage `json:"message"`
	FinishReason ProviderFinishReason            `json:"finish_reason"`
}

type openAICompatibleResponseMessage struct {
	Role      ProviderRole               `json:"role"`
	Content   *string                    `json:"content"`
	ToolCalls []openAICompatibleToolCall `json:"tool_calls"`
}

type openAICompatibleUsage struct {
	PromptTokens        *uint64                             `json:"prompt_tokens"`
	CompletionTokens    *uint64                             `json:"completion_tokens"`
	TotalTokens         *uint64                             `json:"total_tokens"`
	CachedTokens        *uint64                             `json:"cached_tokens"`
	PromptTokensDetails *openAICompatiblePromptTokenDetails `json:"prompt_tokens_details"`
}

type openAICompatiblePromptTokenDetails struct {
	CachedTokens *uint64 `json:"cached_tokens"`
}

func encodeOpenAICompatibleRequest(request ProviderRequest) ([]byte, error) {
	wire := openAICompatibleRequest{
		Model: request.Model, ToolChoice: request.ToolChoice,
		MaxCompletionTokens: request.MaxOutputTokens, Stream: false,
		Messages: make([]openAICompatibleMessage, len(request.Messages)),
		Tools:    make([]openAICompatibleTool, len(request.Tools)),
	}
	for index, message := range request.Messages {
		wireMessage := openAICompatibleMessage{Role: message.Role, ToolCallID: message.ToolCallID}
		if message.Content != "" {
			content := message.Content
			wireMessage.Content = &content
		}
		wireMessage.ToolCalls = make([]openAICompatibleToolCall, len(message.ToolCalls))
		for callIndex, call := range message.ToolCalls {
			wireMessage.ToolCalls[callIndex] = openAICompatibleToolCall{
				ID: call.ID, Type: "function",
				Function: openAICompatibleFunctionCall{Name: call.Name, Arguments: string(call.Arguments)},
			}
		}
		wire.Messages[index] = wireMessage
	}
	for index, tool := range request.Tools {
		wire.Tools[index] = openAICompatibleTool{
			Type: "function",
			Function: openAICompatibleFunctionTool{
				Name: tool.Name, Description: tool.Description, Parameters: append(json.RawMessage(nil), tool.InputSchema...),
			},
		}
	}
	return json.Marshal(wire)
}

func decodeOpenAICompatibleResponse(encoded []byte) (ProviderResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var wire openAICompatibleResponse
	if err := decoder.Decode(&wire); err != nil {
		return ProviderResponse{}, ErrProviderWireResponse
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ProviderResponse{}, ErrProviderWireResponse
	}
	if len(wire.Choices) != 1 || wire.Choices[0].Index != 0 || wire.Usage == nil ||
		wire.Usage.PromptTokens == nil || wire.Usage.CompletionTokens == nil || wire.Usage.TotalTokens == nil {
		return ProviderResponse{}, ErrProviderWireResponse
	}
	cachedTokens, err := openAICompatibleCachedTokens(wire.Usage)
	if err != nil {
		return ProviderResponse{}, err
	}
	choice := wire.Choices[0]
	message := ProviderMessage{Role: choice.Message.Role}
	if choice.Message.Content != nil {
		message.Content = *choice.Message.Content
	}
	message.ToolCalls = make([]ProviderToolCall, len(choice.Message.ToolCalls))
	for index, call := range choice.Message.ToolCalls {
		if call.Type != "function" {
			return ProviderResponse{}, ErrProviderWireResponse
		}
		message.ToolCalls[index] = ProviderToolCall{
			ID: call.ID, Name: call.Function.Name, Arguments: json.RawMessage(call.Function.Arguments),
		}
	}
	response := ProviderResponse{
		Version: ProviderResponseVersion, Model: wire.Model, Message: message, FinishReason: choice.FinishReason,
		Usage: ProviderUsage{
			InputTokens: *wire.Usage.PromptTokens, CachedInputTokens: cachedTokens,
			OutputTokens: *wire.Usage.CompletionTokens, TotalTokens: *wire.Usage.TotalTokens,
		},
	}
	if err := response.Validate(); err != nil {
		return ProviderResponse{}, ErrProviderWireResponse
	}
	return response, nil
}

func openAICompatibleCachedTokens(usage *openAICompatibleUsage) (uint64, error) {
	var details *uint64
	if usage.PromptTokensDetails != nil {
		details = usage.PromptTokensDetails.CachedTokens
	}
	if usage.CachedTokens != nil && details != nil && *usage.CachedTokens != *details {
		return 0, ErrProviderWireResponse
	}
	if usage.CachedTokens != nil {
		return *usage.CachedTokens, nil
	}
	if details != nil {
		return *details, nil
	}
	return 0, nil
}
