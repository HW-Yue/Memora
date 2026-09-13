// Package embedding calls an OpenAI-compatible /v1/embeddings endpoint.
//
// It works with OpenAI, and with any provider that speaks the same wire format
// (SiliconFlow, DeepSeek-compatible gateways, a local Ollama at
// http://localhost:11434/v1, and so on).
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL    = "https://api.openai.com/v1"
	DefaultModel      = "text-embedding-3-small"
	DefaultDimensions = 1536
)

type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	Dimensions int
	HTTP       *http.Client
}

// FromEnvironment reads MEMORA_EMBEDDING_* (falling back to OPENAI_API_KEY).
// It returns ok=false when no API key is configured and the base URL is not
// local, so vector search stays off rather than failing every write.
func FromEnvironment() (Config, bool) {
	config := Config{
		BaseURL: strings.TrimRight(firstNonEmpty(os.Getenv("MEMORA_EMBEDDING_BASE_URL"), DefaultBaseURL), "/"),
		APIKey:  firstNonEmpty(os.Getenv("MEMORA_EMBEDDING_API_KEY"), os.Getenv("OPENAI_API_KEY")),
		Model:   firstNonEmpty(os.Getenv("MEMORA_EMBEDDING_MODEL"), DefaultModel),
	}
	config.Dimensions = DefaultDimensions
	if value, err := strconv.Atoi(os.Getenv("MEMORA_EMBEDDING_DIMENSIONS")); err == nil && value > 0 {
		config.Dimensions = value
	}
	local := strings.Contains(config.BaseURL, "localhost") || strings.Contains(config.BaseURL, "127.0.0.1")
	if config.APIKey == "" && !local {
		return config, false
	}
	return config, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type Client struct{ config Config }

func New(config Config) (*Client, error) {
	if config.BaseURL == "" || config.Model == "" || config.Dimensions <= 0 {
		return nil, errors.New("embedding client needs a base URL, model and dimensions")
	}
	if config.HTTP == nil {
		config.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{config: config}, nil
}

func (client *Client) Dimensions() int { return client.config.Dimensions }

type request struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type response struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (client *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	body := request{Model: client.config.Model, Input: texts}
	if client.config.Dimensions != DefaultDimensions || strings.HasPrefix(client.config.Model, "text-embedding-3") {
		body.Dimensions = client.config.Dimensions
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.config.BaseURL+"/embeddings", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if client.config.APIKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+client.config.APIKey)
	}
	httpResponse, err := client.config.HTTP.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("embedding request: %w", err)
	}
	defer httpResponse.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(httpResponse.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	var decoded response
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("embedding response (HTTP %d) is not JSON", httpResponse.StatusCode)
	}
	if httpResponse.StatusCode != http.StatusOK || decoded.Error != nil {
		message := http.StatusText(httpResponse.StatusCode)
		if decoded.Error != nil {
			message = decoded.Error.Message
		}
		return nil, fmt.Errorf("embedding request failed (HTTP %d): %s", httpResponse.StatusCode, message)
	}
	vectors := make([][]float32, len(texts))
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(texts) {
			return nil, errors.New("embedding response index is out of range")
		}
		if len(item.Embedding) != client.config.Dimensions {
			return nil, fmt.Errorf("embedding has %d dimensions, configured %d", len(item.Embedding), client.config.Dimensions)
		}
		vectors[item.Index] = item.Embedding
	}
	for index, vector := range vectors {
		if vector == nil {
			return nil, fmt.Errorf("embedding response is missing input %d", index)
		}
	}
	return vectors, nil
}
