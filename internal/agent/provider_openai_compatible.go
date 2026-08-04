package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"
)

const (
	minProviderBodyBytes int64 = 256
	maxProviderBodyBytes int64 = 64 << 20
)

var (
	ErrInvalidOpenAICompatibleProvider = errors.New("OpenAI-compatible Provider configuration is invalid")
	ErrProviderSecretUnavailable       = errors.New("Provider secret is unavailable")
	ErrProviderRequestTooLarge         = errors.New("Provider request exceeds its byte budget")
	ErrProviderResponseTooLarge        = errors.New("Provider response exceeds its byte budget")
	ErrProviderHTTPStatus              = errors.New("Provider returned a non-success HTTP status")
	ErrProviderWireResponse            = errors.New("Provider wire response is invalid")
	ErrProviderTransport               = errors.New("Provider transport failed")
)

type ProviderSecretResolver interface {
	ResolveProviderSecret(context.Context, string) (string, error)
}

// EnvironmentProviderSecretResolver reads a named secret from the process
// environment only when a Provider request is made.
type EnvironmentProviderSecretResolver struct{}

func (EnvironmentProviderSecretResolver) ResolveProviderSecret(ctx context.Context, name string) (string, error) {
	if ctx == nil {
		return "", ErrProviderSecretUnavailable
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validProviderSecretName(name) {
		return "", ErrProviderSecretUnavailable
	}
	value, present := os.LookupEnv(name)
	if !present || !validProviderSecret(value) {
		return "", ErrProviderSecretUnavailable
	}
	return value, nil
}

type OpenAICompatibleProviderConfig struct {
	APIBaseURL       string
	SecretName       string
	Dialect          OpenAICompatibleDialect
	Timeout          time.Duration
	MaxRequestBytes  int64
	MaxResponseBytes int64
	Transport        http.RoundTripper
}

type OpenAICompatibleDialect string

const (
	OpenAICompatibleDialectStandard              OpenAICompatibleDialect = ""
	OpenAICompatibleDialectDeepSeekV4NonThinking OpenAICompatibleDialect = "deepseek-v4-non-thinking"
)

type OpenAICompatibleProvider struct {
	endpoint         string
	secretName       string
	secrets          ProviderSecretResolver
	client           *http.Client
	maxRequestBytes  int64
	maxResponseBytes int64
	dialect          OpenAICompatibleDialect
}

type ProviderHTTPStatusError struct {
	StatusCode int
}

func (err *ProviderHTTPStatusError) Error() string {
	return fmt.Sprintf("%s: %d", ErrProviderHTTPStatus, err.StatusCode)
}

func (err *ProviderHTTPStatusError) Unwrap() error {
	return ErrProviderHTTPStatus
}

func NewOpenAICompatibleProvider(
	config OpenAICompatibleProviderConfig,
	secrets ProviderSecretResolver,
) (*OpenAICompatibleProvider, error) {
	endpoint, err := openAICompatibleEndpoint(config.APIBaseURL)
	if err != nil || secrets == nil || !validProviderSecretName(config.SecretName) ||
		!validOpenAICompatibleDialect(config.Dialect) ||
		config.Timeout <= 0 || config.Timeout > 10*time.Minute ||
		config.MaxRequestBytes < minProviderBodyBytes || config.MaxRequestBytes > maxProviderBodyBytes ||
		config.MaxResponseBytes < minProviderBodyBytes || config.MaxResponseBytes > maxProviderBodyBytes {
		return nil, ErrInvalidOpenAICompatibleProvider
	}
	transport := config.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &OpenAICompatibleProvider{
		endpoint: endpoint, secretName: config.SecretName, secrets: secrets,
		client: &http.Client{
			Transport: transport,
			Timeout:   config.Timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxRequestBytes: config.MaxRequestBytes, maxResponseBytes: config.MaxResponseBytes,
		dialect: config.Dialect,
	}, nil
}

func (provider *OpenAICompatibleProvider) Complete(
	ctx context.Context,
	request ProviderRequest,
) (ProviderResponse, error) {
	if provider == nil || provider.client == nil || provider.secrets == nil || ctx == nil {
		return ProviderResponse{}, ErrInvalidOpenAICompatibleProvider
	}
	if err := request.Validate(); err != nil {
		return ProviderResponse{}, err
	}
	body, err := encodeOpenAICompatibleRequest(request, provider.dialect)
	if err != nil {
		return ProviderResponse{}, ErrProviderWireResponse
	}
	if int64(len(body)) > provider.maxRequestBytes {
		return ProviderResponse{}, ErrProviderRequestTooLarge
	}
	secret, err := provider.secrets.ResolveProviderSecret(ctx, provider.secretName)
	if err != nil || !validProviderSecret(secret) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ProviderResponse{}, ctxErr
		}
		if errors.Is(err, context.Canceled) {
			return ProviderResponse{}, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return ProviderResponse{}, context.DeadlineExceeded
		}
		return ProviderResponse{}, ErrProviderSecretUnavailable
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.endpoint, bytes.NewReader(body))
	if err != nil {
		return ProviderResponse{}, ErrProviderTransport
	}
	httpRequest.Header.Set("Authorization", "Bearer "+secret)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpResponse, err := provider.client.Do(httpRequest)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ProviderResponse{}, ctxErr
		}
		if errors.Is(err, context.Canceled) {
			return ProviderResponse{}, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return ProviderResponse{}, context.DeadlineExceeded
		}
		return ProviderResponse{}, ErrProviderTransport
	}
	defer httpResponse.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, provider.maxResponseBytes+1))
	if err != nil {
		return ProviderResponse{}, ErrProviderTransport
	}
	if int64(len(responseBody)) > provider.maxResponseBytes {
		return ProviderResponse{}, ErrProviderResponseTooLarge
	}
	if httpResponse.StatusCode < http.StatusOK || httpResponse.StatusCode >= http.StatusMultipleChoices {
		return ProviderResponse{}, &ProviderHTTPStatusError{StatusCode: httpResponse.StatusCode}
	}
	response, err := decodeOpenAICompatibleResponse(responseBody)
	if err != nil || response.Model != request.Model {
		return ProviderResponse{}, ErrProviderWireResponse
	}
	return response, nil
}

func validOpenAICompatibleDialect(dialect OpenAICompatibleDialect) bool {
	return dialect == OpenAICompatibleDialectStandard || dialect == OpenAICompatibleDialectDeepSeekV4NonThinking
}

func openAICompatibleEndpoint(baseURL string) (string, error) {
	if strings.TrimSpace(baseURL) != baseURL || len(baseURL) == 0 || len(baseURL) > 2048 {
		return "", ErrInvalidOpenAICompatibleProvider
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return "", ErrInvalidOpenAICompatibleProvider
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return "", ErrInvalidOpenAICompatibleProvider
		}
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		address := net.ParseIP(parsed.Hostname())
		if address == nil || !address.IsLoopback() {
			return "", ErrInvalidOpenAICompatibleProvider
		}
	default:
		return "", ErrInvalidOpenAICompatibleProvider
	}
	return parsed.JoinPath("chat", "completions").String(), nil
}

func validProviderSecretName(name string) bool {
	if len(name) == 0 || len(name) > 80 || name[0] < 'A' || name[0] > 'Z' {
		return false
	}
	for _, character := range []byte(name) {
		if character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validProviderSecret(secret string) bool {
	return len(secret) > 0 && len(secret) <= 16<<10 && strings.IndexFunc(secret, unicode.IsSpace) < 0 &&
		!strings.ContainsRune(secret, '\x00')
}

var _ Provider = (*OpenAICompatibleProvider)(nil)
var _ ProviderSecretResolver = EnvironmentProviderSecretResolver{}
