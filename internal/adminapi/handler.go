package adminapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/readquery"
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
		statements[index].Authorization = security.Authorization{
			Version:             security.AuthorizationVersion,
			Actor:               "user:admin",
			AuthorizedDatabases: append([]string(nil), gateway.scopes...),
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
