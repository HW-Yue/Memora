package benchmark

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/result"
)

type ModeledRecord struct {
	ID      string   `json:"id"`
	Durable bool     `json:"durable"`
	Terms   []string `json:"terms"`
}

type ModeledQuery struct {
	ID    string   `json:"id"`
	Terms []string `json:"terms"`
}

type ModelSnapshot struct {
	Records  []ModeledRecord `json:"records"`
	Queries  []ModeledQuery  `json:"queries"`
	Evidence Evidence        `json:"evidence"`
}

type SemanticModeler interface {
	Model(context.Context, ReleaseCorpus) (ModelSnapshot, error)
}

type OpenAIModelerConfig struct {
	BaseURL  string
	APIKey   string
	Model    string
	Provider string
	Timeout  time.Duration
}

type OpenAIModeler struct {
	config OpenAIModelerConfig
	client *http.Client
}

func NewOpenAIModeler(config OpenAIModelerConfig) (*OpenAIModeler, error) {
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.Model = strings.TrimSpace(config.Model)
	config.Provider = strings.TrimSpace(config.Provider)
	if config.BaseURL == "" || config.APIKey == "" || config.Model == "" {
		return nil, benchmarkError(result.CodeValidation, "model base URL, API key, and model are required")
	}
	if config.Timeout <= 0 {
		config.Timeout = 3 * time.Minute
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return nil, benchmarkError(result.CodeValidation, "model base URL must be an absolute HTTPS URL")
	}
	if config.Provider == "" {
		config.Provider = parsed.Hostname()
	}
	return &OpenAIModeler{config: config, client: &http.Client{Timeout: config.Timeout}}, nil
}

func (modeler *OpenAIModeler) Model(ctx context.Context, corpus ReleaseCorpus) (ModelSnapshot, error) {
	if modeler == nil || modeler.client == nil {
		return ModelSnapshot{}, benchmarkError(result.CodeInternal, "semantic modeler is not configured")
	}
	if err := corpus.Validate(); err != nil {
		return ModelSnapshot{}, err
	}
	type promptItem struct {
		ID      string `json:"id"`
		Project string `json:"project"`
		Text    string `json:"text"`
	}
	records := make([]promptItem, 0, len(corpus.Records))
	for _, record := range corpus.Records {
		records = append(records, promptItem{ID: record.ID, Project: record.Project, Text: record.Text})
	}
	queries := make([]promptItem, 0, len(corpus.Queries))
	for _, query := range corpus.Queries {
		queries = append(queries, promptItem{ID: query.ID, Project: query.Project, Text: query.Text})
	}
	var modeled struct {
		Records []ModeledRecord `json:"records"`
		Queries []ModeledQuery  `json:"queries"`
	}
	var contents []string
	var inputTokens, outputTokens uint64
	const batchSize = 10
	for start := 0; start < len(records); start += batchSize {
		end := min(start+batchSize, len(records))
		input, err := json.Marshal(map[string]any{"records": records[start:end]})
		if err != nil {
			return ModelSnapshot{}, benchmarkError(result.CodeInternal, "encode record model input: %v", err)
		}
		prompt := `You are the semantic modeling layer of a personal database benchmark.
Return one JSON object only: {"records":[{"id":"...","durable":true,"terms":["..."]}]}.
Return only records worth writing to durable memory; omission is the explicit reject decision.
Durable means stable future-useful state, a reviewed semantic conclusion, decision,
preference, policy, or revision. Greetings, one-time codes, current prices/build
timings/weather, unreviewed raw excerpts, and stale unconfirmed mutations are not durable.
For every accepted ID emit 4-10 short semantic discovery terms with useful paraphrases;
never copy secrets or invent facts. Input: ` + string(input)
		content, promptCount, completionCount, err := modeler.complete(ctx, prompt)
		if err != nil {
			return ModelSnapshot{}, err
		}
		var batch struct {
			Records []ModeledRecord `json:"records"`
		}
		if err := decodeModeledContent(content, &batch); err != nil {
			return ModelSnapshot{}, err
		}
		modeled.Records = append(modeled.Records, batch.Records...)
		contents = append(contents, content)
		inputTokens += promptCount
		outputTokens += completionCount
	}
	for start := 0; start < len(queries); start += batchSize {
		end := min(start+batchSize, len(queries))
		input, err := json.Marshal(map[string]any{"queries": queries[start:end]})
		if err != nil {
			return ModelSnapshot{}, benchmarkError(result.CodeInternal, "encode query model input: %v", err)
		}
		prompt := `You are the semantic routing layer of a personal database benchmark.
Return one JSON object only: {"queries":[{"id":"...","terms":["..."]}]}.
Preserve every supplied ID exactly once. For each query emit 4-10 short semantic discovery
terms and useful paraphrases that a personal database could match. Do not answer the query,
copy secrets, or invent facts. Input: ` + string(input)
		content, promptCount, completionCount, err := modeler.complete(ctx, prompt)
		if err != nil {
			return ModelSnapshot{}, err
		}
		var batch struct {
			Queries []ModeledQuery `json:"queries"`
		}
		if err := decodeModeledContent(content, &batch); err != nil {
			return ModelSnapshot{}, err
		}
		if len(batch.Queries) != end-start {
			return ModelSnapshot{}, benchmarkError(
				result.CodeValidation,
				"model query batch %d returned %d observations, want %d",
				start/batchSize, len(batch.Queries), end-start,
			)
		}
		modeled.Queries = append(modeled.Queries, batch.Queries...)
		contents = append(contents, content)
		inputTokens += promptCount
		outputTokens += completionCount
	}
	if err := validateModelSnapshot(corpus, modeled.Records, modeled.Queries); err != nil {
		return ModelSnapshot{}, err
	}
	parsed, _ := url.Parse(modeler.config.BaseURL)
	corpusDigest, err := canonicalDigest(corpus)
	if err != nil {
		return ModelSnapshot{}, err
	}
	sum := sha256.Sum256([]byte(strings.Join(contents, "\n")))
	return ModelSnapshot{
		Records: modeled.Records, Queries: modeled.Queries,
		Evidence: Evidence{
			Mode: "live-model", Protocol: "openai-compatible", Provider: modeler.config.Provider,
			EndpointHost: parsed.Hostname(), Model: modeler.config.Model,
			InputTokens: inputTokens, OutputTokens: outputTokens,
			OutputDigest: hex.EncodeToString(sum[:]), CorpusDigest: corpusDigest,
			Implementation: "openai-compatible-semantic-modeler/v1",
		},
	}, nil
}

func (modeler *OpenAIModeler) complete(ctx context.Context, prompt string) (string, uint64, uint64, error) {
	payload, err := json.Marshal(map[string]any{
		"model": modeler.config.Model, "temperature": 0, "max_tokens": 3000,
		"response_format": map[string]string{"type": "json_object"},
		"messages":        []map[string]string{{"role": "user", "content": prompt}},
	})
	if err != nil {
		return "", 0, 0, benchmarkError(result.CodeInternal, "encode model request: %v", err)
	}
	endpoint := modeler.config.BaseURL
	if !strings.HasSuffix(endpoint, "/v1") {
		endpoint += "/v1"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", 0, 0, benchmarkError(result.CodeInternal, "create model request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+modeler.config.APIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := modeler.client.Do(request)
	if err != nil {
		return "", 0, 0, benchmarkError(result.CodeInternal, "model request failed: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return "", 0, 0, benchmarkError(result.CodeInternal, "read model response: %v", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", 0, 0, benchmarkError(result.CodeInternal, "model response status %d: %s", response.StatusCode, safeModelError(body))
	}
	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     uint64 `json:"prompt_tokens"`
			CompletionTokens uint64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &completion); err != nil || len(completion.Choices) != 1 {
		return "", 0, 0, benchmarkError(result.CodeValidation, "decode model completion: %v", err)
	}
	if completion.Choices[0].FinishReason != "" && completion.Choices[0].FinishReason != "stop" {
		return "", 0, 0, benchmarkError(
			result.CodeValidation,
			"model completion ended with %q instead of stop",
			completion.Choices[0].FinishReason,
		)
	}
	return stripJSONFence(completion.Choices[0].Message.Content),
		completion.Usage.PromptTokens, completion.Usage.CompletionTokens, nil
}

func decodeModeledContent(content string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return benchmarkError(result.CodeValidation, "decode modeled observations: %v", err)
	}
	return nil
}

func validateModelSnapshot(corpus ReleaseCorpus, records []ModeledRecord, queries []ModeledQuery) error {
	recordIDs, queryIDs := map[string]bool{}, map[string]bool{}
	for _, record := range corpus.Records {
		recordIDs[record.ID] = true
	}
	for _, query := range corpus.Queries {
		queryIDs[query.ID] = true
	}
	seen := map[string]bool{}
	for _, record := range records {
		if !record.Durable || !recordIDs[record.ID] || seen[record.ID] || !validTerms(record.Terms) {
			return benchmarkError(result.CodeValidation, "model returned invalid record observation %q", record.ID)
		}
		seen[record.ID] = true
	}
	seen = map[string]bool{}
	for _, query := range queries {
		if !queryIDs[query.ID] || seen[query.ID] || !validTerms(query.Terms) {
			return benchmarkError(result.CodeValidation, "model returned invalid query observation %q", query.ID)
		}
		seen[query.ID] = true
	}
	if len(seen) != len(queryIDs) {
		return benchmarkError(result.CodeValidation, "model returned %d query observations, want %d", len(seen), len(queryIDs))
	}
	return nil
}

func validTerms(terms []string) bool {
	if len(terms) < 1 || len(terms) > 20 {
		return false
	}
	for _, term := range terms {
		if strings.TrimSpace(term) == "" || len(term) > 200 {
			return false
		}
	}
	return true
}

func stripJSONFence(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimPrefix(content, "```JSON")
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(strings.TrimSpace(content), "```")
	}
	return strings.TrimSpace(content)
}

func safeModelError(body []byte) string {
	var response struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &response) == nil && response.Error.Message != "" {
		return response.Error.Message
	}
	return fmt.Sprintf("%d-byte response", len(body))
}
