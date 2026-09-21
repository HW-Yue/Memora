package adminapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/readquery"
	"github.com/HW-Yue/Memora/internal/recall"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

type readInput struct {
	Parameters executor.Parameters `json:"parameters,omitempty"`
}

type readRequest struct {
	Source     string      `json:"source"`
	Statements []readInput `json:"statements,omitempty"`
}

type apiError struct {
	Version string `json:"version"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (gateway *Gateway) shellHandler(shell http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !gateway.validHost(request) {
			secureHeaders(response)
			writeAPIError(response, http.StatusForbidden, "forbidden", "request host is not allowed")
			return
		}
		shell.ServeHTTP(response, request)
	})
}

func (gateway *Gateway) handleUnknownAPI(response http.ResponseWriter, request *http.Request) {
	secureHeaders(response)
	if !gateway.validHost(request) {
		writeAPIError(response, http.StatusForbidden, "forbidden", "request host is not allowed")
		return
	}
	writeAPIError(response, http.StatusNotFound, "not_found", "API endpoint was not found")
}

func (gateway *Gateway) handleSession(response http.ResponseWriter, request *http.Request) {
	secureHeaders(response)
	if !gateway.validHost(request) {
		writeAPIError(response, http.StatusForbidden, "forbidden", "request host is not allowed")
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if !gateway.validOrigin(request) {
		writeAPIError(response, http.StatusForbidden, "forbidden", "request origin is not allowed")
		return
	}
	if !jsonContentType(request) {
		writeAPIError(response, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return
	}
	var empty struct{}
	if err := decodeStrict(response, request, &empty); err != nil {
		writeDecodeError(response, err)
		return
	}
	token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.expired() {
		writeAPIError(response, http.StatusUnauthorized, "unauthorized", "admin session is invalid")
		return
	}
	if cookie, err := request.Cookie(sessionCookie); err == nil &&
		sameToken(gateway.sessionHash, cookie.Value) {
		writeJSON(response, http.StatusOK, SessionReceipt{
			Version: SessionVersion, CSRFToken: gateway.csrfToken,
			ExpiresAt: gateway.descriptor.ExpiresAt,
		})
		return
	}
	if gateway.bootstrapConsumed || token == "" ||
		!sameToken(gateway.bootstrapHash, token) {
		writeAPIError(response, http.StatusUnauthorized, "unauthorized", "session bootstrap is invalid")
		return
	}
	gateway.bootstrapConsumed = true
	gateway.bootstrapHash = [sha256.Size]byte{}
	http.SetCookie(response, &http.Cookie{
		Name:     sessionCookie,
		Value:    gateway.sessionToken,
		Path:     "/",
		Expires:  gateway.descriptor.ExpiresAt,
		MaxAge:   max(1, int(gateway.descriptor.ExpiresAt.Sub(gateway.now()).Seconds())),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	gateway.sessionToken = ""
	writeJSON(response, http.StatusOK, SessionReceipt{
		Version:   SessionVersion,
		CSRFToken: gateway.csrfToken,
		ExpiresAt: gateway.descriptor.ExpiresAt,
	})
}

func (gateway *Gateway) handleMSQL(response http.ResponseWriter, request *http.Request) {
	secureHeaders(response)
	if !gateway.validHost(request) {
		writeAPIError(response, http.StatusForbidden, "forbidden", "request host is not allowed")
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if !gateway.validOrigin(request) {
		writeAPIError(response, http.StatusForbidden, "forbidden", "request origin is not allowed")
		return
	}
	if !jsonContentType(request) {
		writeAPIError(response, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return
	}
	if !gateway.validSession(request) {
		writeAPIError(response, http.StatusUnauthorized, "unauthorized", "admin session is invalid")
		return
	}
	if !gateway.validCSRF(request.Header.Get("X-Memora-CSRF")) {
		writeAPIError(response, http.StatusForbidden, "forbidden", "CSRF token is invalid")
		return
	}
	var input readRequest
	if err := decodeStrict(response, request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	count, err := readquery.Validate(input.Source)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, "not_read_only", "MSQL batch is not read-only")
		return
	}
	if count > maxStatements {
		writeAPIError(response, http.StatusBadRequest, "too_many_statements", "MSQL batch exceeds 32 statements")
		return
	}
	if len(input.Statements) != 0 && len(input.Statements) != count {
		writeAPIError(response, http.StatusBadRequest, "invalid_request", "statement input count does not match MSQL batch")
		return
	}
	statements := make([]executor.StatementInput, count)
	for index := range statements {
		if len(input.Statements) != 0 {
			statements[index].Parameters = input.Statements[index].Parameters
		}
		if len(gateway.scopes) != 0 {
			statements[index].Authorization = security.Authorization{
				Version:             security.AuthorizationVersion,
				Actor:               "user:admin",
				AuthorizedDatabases: append([]string(nil), gateway.scopes...),
			}
		}
	}
	envelope, err := gateway.execute(request.Context(), gateway.dataDir, input.Source, statements)
	if err != nil {
		writeAPIError(response, http.StatusBadGateway, "daemon_unavailable", "database daemon request failed")
		return
	}
	writeJSON(response, http.StatusOK, envelope)
}

func (gateway *Gateway) validHost(request *http.Request) bool {
	return request.Host == strings.TrimPrefix(gateway.descriptor.Origin, "http://")
}

func (gateway *Gateway) validOrigin(request *http.Request) bool {
	return request.Header.Get("Origin") == gateway.descriptor.Origin
}

func (gateway *Gateway) validSession(request *http.Request) bool {
	if gateway.expired() {
		return false
	}
	cookie, err := request.Cookie(sessionCookie)
	return err == nil && sameToken(gateway.sessionHash, cookie.Value)
}

func (gateway *Gateway) validCSRF(token string) bool {
	return token != "" && sameToken(gateway.csrfHash, token)
}

func (gateway *Gateway) expired() bool {
	return !gateway.now().Before(gateway.descriptor.ExpiresAt)
}

func sameToken(want [sha256.Size]byte, supplied string) bool {
	got := sha256.Sum256([]byte(supplied))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}

func jsonContentType(request *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

func decodeStrict(response http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(response, request.Body, MaxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeDecodeError(response http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeAPIError(response, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds 256 KiB")
		return
	}
	writeAPIError(response, http.StatusBadRequest, "invalid_json", "request body must be one strict JSON object")
}

func secureHeaders(response http.ResponseWriter) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeAPIError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, apiError{Version: ErrorVersion, Code: code, Message: message})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

// The search page's data face. It exists at the gateway for one reason: a vector
// query has to be embedded, the engine never calls a model, and the page is a
// read-only MSQL client. So the gateway — which is a host, with the host's
// provider — turns the words into a vector and asks the engine for the fused
// listing.
//
// Two things this deliberately does not do. It does not invent an order: the
// engine fuses by rank (RRF) and the gateway returns that listing untouched. And
// it does not hide a degraded arm: the receipt carries whether the vector arm ran
// and, when it did not, which of the three quite different situations happened —
// nobody configured a provider (stable), the provider failed this time
// (transient), or the Database cannot answer a vector query yet (per Database).

const (
	// SearchVersion identifies the search receipt.
	SearchVersion = "memora.admin-search/v1"
	// searchLimit is the page's top-N. It is the statement's LIMIT, so it is a
	// listing truncation, not a recall strength.
	searchLimit = 10
	// searchEmbeddingTimeout bounds the provider call. A slow provider costs the
	// page its vector arm, never its answer.
	searchEmbeddingTimeout = 5 * time.Second
)

type searchRequest struct {
	Database string `json:"database"`
	Query    string `json:"query"`
}

type searchVector struct {
	Ran        bool   `json:"ran"`
	Reason     string `json:"reason,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Model      string `json:"model,omitempty"`
	Dimensions int    `json:"dimensions,omitempty"`
}

type searchReceipt struct {
	Version   string          `json:"version"`
	Database  string          `json:"database"`
	Query     string          `json:"query"`
	Source    string          `json:"source"`
	Vector    searchVector    `json:"vector"`
	Rows      []result.Row    `json:"rows"`
	Truncated bool            `json:"truncated"`
	Warnings  []result.Notice `json:"warnings"`
}

func (gateway *Gateway) handleSearch(response http.ResponseWriter, request *http.Request) {
	secureHeaders(response)
	if !gateway.validHost(request) {
		writeAPIError(response, http.StatusForbidden, "forbidden", "request host is not allowed")
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if !gateway.validOrigin(request) {
		writeAPIError(response, http.StatusForbidden, "forbidden", "request origin is not allowed")
		return
	}
	if !jsonContentType(request) {
		writeAPIError(response, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return
	}
	if !gateway.validSession(request) {
		writeAPIError(response, http.StatusUnauthorized, "unauthorized", "admin session is invalid")
		return
	}
	if !gateway.validCSRF(request.Header.Get("X-Memora-CSRF")) {
		writeAPIError(response, http.StatusForbidden, "forbidden", "CSRF token is invalid")
		return
	}
	var input searchRequest
	if err := decodeStrict(response, request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	database := strings.TrimSpace(input.Database)
	query := strings.TrimSpace(input.Query)
	if database == "" || query == "" {
		writeAPIError(response, http.StatusBadRequest, "invalid_request", "search needs a database and a query")
		return
	}
	receipt := searchReceipt{Version: SearchVersion, Database: database, Query: query}
	named := map[string]any{"query": query, "limit": searchLimit}
	source := recallStatement("MATCH :query", database)

	if gateway.embedder == nil {
		receipt.Vector = searchVector{Reason: "not_configured"}
	} else if embedded, status := gateway.embedQuery(request.Context(), query); status == nil {
		receipt.Vector = searchVector{Ran: true, Model: gateway.embedder.Model(), Dimensions: gateway.embedder.Dimensions()}
		named["nearest"] = embedded
		fused := recallStatement("MATCH :query NEAREST :nearest", database)
		result, failure := gateway.readMSQL(request.Context(), fused, named)
		if failure == nil {
			receipt.Source = fused
			receipt.Rows, receipt.Truncated, receipt.Warnings = result.Rows, result.Truncated, result.Warnings
			writeJSON(response, http.StatusOK, receipt)
			return
		}
		// The provider answered but this Database cannot answer a vector query —
		// no identity yet, another model locked, or a rekey in progress. A stable
		// condition, reported as one, with the keyword listing as the answer.
		receipt.Vector = searchVector{Reason: "vector_not_ready", Detail: failure.Message}
	} else {
		receipt.Vector = *status
	}

	result, failure := gateway.readMSQL(request.Context(), source, named)
	if failure != nil {
		writeAPIError(response, http.StatusBadRequest, string(failure.Code), failure.Message)
		return
	}
	receipt.Source = source
	receipt.Rows, receipt.Truncated, receipt.Warnings = result.Rows, result.Truncated, result.Warnings
	writeJSON(response, http.StatusOK, receipt)
}

// embedQuery embeds the words, or explains why the vector arm is not running.
func (gateway *Gateway) embedQuery(ctx context.Context, query string) (string, *searchVector) {
	bounded, cancel := context.WithTimeout(ctx, searchEmbeddingTimeout)
	defer cancel()
	vectors, err := gateway.embedder.Embed(bounded, []string{query})
	if err != nil {
		detail := err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			return "", &searchVector{Reason: "embedding_timeout", Detail: detail}
		}
		return "", &searchVector{Reason: "provider_unavailable", Detail: detail}
	}
	if len(vectors) != 1 || len(vectors[0]) != gateway.embedder.Dimensions() {
		return "", &searchVector{Reason: "provider_unavailable",
			Detail: fmt.Sprintf("provider returned %d vector(s) of unexpected width", len(vectors))}
	}
	encoded, err := recall.EncodeVector(vectors[0])
	if err != nil {
		return "", &searchVector{Reason: "provider_unavailable", Detail: err.Error()}
	}
	return encoded, nil
}

// readMSQL runs one read-only statement as the Admin session does: the same
// scope, the same read-only gate, the same daemon round trip.
func (gateway *Gateway) readMSQL(ctx context.Context, source string, named map[string]any) (result.StatementResult, *result.ResultError) {
	if _, err := readquery.Validate(source); err != nil {
		return result.StatementResult{}, &result.ResultError{Code: result.CodeUnsupported, Message: "statement is not read-only"}
	}
	statement := executor.StatementInput{Parameters: executor.Parameters{Named: named}}
	if len(gateway.scopes) != 0 {
		statement.Authorization = security.Authorization{
			Version:             security.AuthorizationVersion,
			Actor:               "user:admin",
			AuthorizedDatabases: append([]string(nil), gateway.scopes...),
		}
	}
	envelope, err := gateway.execute(ctx, gateway.dataDir, source, []executor.StatementInput{statement})
	if err != nil {
		return result.StatementResult{}, &result.ResultError{Code: result.CodeInternal, Message: "database daemon request failed"}
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return result.StatementResult{}, envelope.Error
		}
		return result.StatementResult{}, &result.ResultError{Code: result.CodeInternal, Message: "database request failed"}
	}
	if len(envelope.Results) != 1 || envelope.Results[0].Error != nil {
		if len(envelope.Results) == 1 {
			return result.StatementResult{}, envelope.Results[0].Error
		}
		return result.StatementResult{}, &result.ResultError{Code: result.CodeInternal, Message: "database request failed"}
	}
	return envelope.Results[0], nil
}

func recallStatement(arm, database string) string {
	return fmt.Sprintf("RECALL FROM %q %s LIMIT :limit", database, arm)
}
