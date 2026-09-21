// Package embedding calls the embedding provider the host configured.
//
// It sits on the host side of a line the product draws deliberately: Memora's
// engine never computes a vector, never holds a provider URL or a key, and never
// reaches the network. This package is what the CLI uses to do the host's part —
// turn text into vectors — and it is the only place in the tree that would ever
// open an outbound connection.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The host configures embeddings with environment variables, the same way it
// configures any other tool. They are read at use time, not captured in a file
// the engine could read.
const (
	EnvSwitch     = "MEMORA_EMBEDDING"
	EnvBaseURL    = "MEMORA_EMBEDDING_BASE_URL"
	EnvModel      = "MEMORA_EMBEDDING_MODEL"
	EnvDimensions = "MEMORA_EMBEDDING_DIMENSIONS"
	EnvAPIKey     = "MEMORA_EMBEDDING_API_KEY"
)

// Off is the value that turns embeddings off even when everything else is set.
const Off = "off"

// Config is one host's embedding provider.
type Config struct {
	BaseURL    string
	Model      string
	Dimensions int
	APIKey     string
}

// Configured reports whether there is enough here to embed anything.
func (config Config) Configured() bool {
	return config.BaseURL != "" && config.Model != "" && config.Dimensions > 0 && config.APIKey != ""
}

// ConfigFromEnv reads the host's configuration.
//
// "Not configured" and "turned off" deliberately end up in the same place — an
// empty Config — because from the engine's point of view they are the same
// thing: nothing will be embedded, and every unit stays not-ready until a host
// with a provider drains it.
//
// A partly configured provider is an error rather than a silent skip: someone
// who set a base URL and a model but no key wants to hear about the missing key,
// not to discover months later that nothing was ever embedded. The error names
// the variables that are missing and never their values.
func ConfigFromEnv(lookup func(string) string) (Config, error) {
	if strings.EqualFold(strings.TrimSpace(lookup(EnvSwitch)), Off) {
		return Config{}, nil
	}
	config := Config{
		BaseURL: strings.TrimSpace(lookup(EnvBaseURL)),
		Model:   strings.TrimSpace(lookup(EnvModel)),
		APIKey:  strings.TrimSpace(lookup(EnvAPIKey)),
	}
	dimensions := strings.TrimSpace(lookup(EnvDimensions))
	if dimensions != "" {
		parsed, err := strconv.Atoi(dimensions)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive integer", EnvDimensions)
		}
		config.Dimensions = parsed
	}
	missing := []string{}
	for name, value := range map[string]string{
		EnvBaseURL: config.BaseURL, EnvModel: config.Model,
		EnvDimensions: dimensions, EnvAPIKey: config.APIKey,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return config, nil
	}
	// Nothing set at all is the ordinary case: embeddings are simply not in use.
	if len(missing) == 4 {
		return Config{}, nil
	}
	slices.Sort(missing)
	return Config{}, fmt.Errorf("embedding is partly configured; also set %s, or set %s=%s to turn it off",
		strings.Join(missing, ", "), EnvSwitch, Off)
}

// Embedder turns text into vectors. The CLI depends on this rather than on the
// HTTP client, so a build (and a test) can run without a provider at all.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Client is an OpenAI-compatible embeddings endpoint.
type Client struct {
	httpClient *http.Client
	config     Config
}

// New builds a client. The HTTP client is injectable so that nothing in the tree
// needs a socket to test what this does.
func New(config Config, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{httpClient: httpClient, config: config}
}

// NewFromEnv builds a client from the environment, or returns nil when the host
// has not configured embeddings. A nil Embedder means "there is nothing to do
// here", which callers must treat as the ordinary case rather than a failure.
func NewFromEnv() (Embedder, Config, error) {
	config, err := ConfigFromEnv(os.Getenv)
	if err != nil {
		return nil, Config{}, err
	}
	if !config.Configured() {
		return nil, config, nil
	}
	return New(config, nil), config, nil
}

// Dimensions reports the width every vector from this provider will have. The
// engine checks it against the Database's identity, so a provider changed
// without re-embedding is refused rather than mixed in.
func (client *Client) Dimensions() int { return client.config.Dimensions }

type embedRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed asks the provider for one vector per text, in the order they were given.
func (client *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if !client.config.Configured() {
		return nil, fmt.Errorf("embedding is not configured")
	}
	body, err := json.Marshal(embedRequest{
		Model: client.config.Model, Input: texts, Dimensions: client.config.Dimensions,
	})
	if err != nil {
		return nil, fmt.Errorf("encode embedding request: %w", err)
	}
	endpoint := strings.TrimSuffix(client.config.BaseURL, "/") + "/embeddings"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build embedding request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.config.APIKey)
	response, err := client.httpClient.Do(request)
	if err != nil {
		// The error from the transport can carry the URL; it never carries the
		// key, and neither does this message.
		return nil, fmt.Errorf("call the embedding provider: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		// The body is read and dropped rather than forwarded: a provider that
		// echoes the request on error would otherwise put the key in a log.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		return nil, fmt.Errorf("the embedding provider answered %s", response.Status)
	}
	payload := embedResponse{}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("read embedding response: %w", err)
	}
	if len(payload.Data) != len(texts) {
		return nil, fmt.Errorf("the embedding provider returned %d vectors for %d inputs",
			len(payload.Data), len(texts))
	}
	vectors := make([][]float32, len(texts))
	for _, item := range payload.Data {
		if item.Index < 0 || item.Index >= len(vectors) {
			return nil, fmt.Errorf("the embedding provider returned an out-of-range index")
		}
		vectors[item.Index] = item.Embedding
	}
	for _, vector := range vectors {
		if len(vector) != client.config.Dimensions {
			return nil, fmt.Errorf("the embedding provider returned %d dimensions, not %d",
				len(vector), client.config.Dimensions)
		}
	}
	return vectors, nil
}
