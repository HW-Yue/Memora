package security

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/result"
)

const (
	AuthorizationVersion = "memora.authorization/v1"
	ApprovalVersion      = "memora.approval/v1"
	ActionInstallPackage = "INSTALL_PACKAGE"
	maxDatabaseScope     = 32
)

type Authorization struct {
	Version             string    `json:"version"`
	Actor               string    `json:"actor"`
	AuthorizedDatabases []string  `json:"authorized_databases"`
	Approval            *Approval `json:"approval,omitempty"`
}

type Approval struct {
	Version       string `json:"version"`
	Action        string `json:"action"`
	SubjectSHA256 string `json:"subject_sha256"`
	Confirmed     bool   `json:"confirmed"`
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return "security: " + err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

type authorizationKey struct{}

func WithAuthorization(ctx context.Context, authorization Authorization) context.Context {
	return context.WithValue(ctx, authorizationKey{}, authorization)
}

func AuthorizationFrom(ctx context.Context) (Authorization, bool) {
	if ctx == nil {
		return Authorization{}, false
	}
	authorization, ok := ctx.Value(authorizationKey{}).(Authorization)
	return authorization, ok
}

func (authorization Authorization) Validate() error {
	if authorization.Version != AuthorizationVersion {
		return securityError(result.CodeValidation, "unsupported Authorization version")
	}
	if invalidText(authorization.Actor, 160, true) {
		return securityError(result.CodeValidation, "Authorization actor is invalid")
	}
	if len(authorization.AuthorizedDatabases) == 0 && authorization.Approval == nil {
		return securityError(result.CodePermissionDenied, "Authorization requires a Database scope or approval")
	}
	if len(authorization.AuthorizedDatabases) > maxDatabaseScope {
		return securityError(result.CodePermissionDenied, "Authorization exceeds 32 Database selectors")
	}
	seen := map[string]bool{}
	for _, database := range authorization.AuthorizedDatabases {
		normalized := canonical(database)
		if invalidText(database, 200, true) || normalized == "" || seen[normalized] {
			return securityError(result.CodeValidation, "Authorization Database scope is invalid or duplicated")
		}
		seen[normalized] = true
	}
	if authorization.Approval != nil {
		if err := authorization.Approval.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (approval Approval) Validate() error {
	if approval.Version != ApprovalVersion || invalidText(approval.Action, 80, true) ||
		!validSHA256(approval.SubjectSHA256) {
		return securityError(result.CodeValidation, "approval is invalid")
	}
	if !approval.Confirmed {
		return securityError(result.CodePermissionDenied, "approval is not confirmed")
	}
	return nil
}

func RequireAuthorization(ctx context.Context) (Authorization, error) {
	authorization, ok := AuthorizationFrom(ctx)
	if !ok {
		return Authorization{}, securityError(result.CodePermissionDenied, "operation requires explicit Authorization")
	}
	if err := authorization.Validate(); err != nil {
		return Authorization{}, err
	}
	return authorization, nil
}

func RequireDatabase(ctx context.Context, database string) error {
	return RequireAnyDatabase(ctx, database)
}

func RequireAnyDatabase(ctx context.Context, databases ...string) error {
	authorization, err := RequireAuthorization(ctx)
	if err != nil {
		return err
	}
	if AllowsAnyDatabase(authorization, databases...) {
		return nil
	}
	return securityError(
		result.CodePermissionDenied,
		fmt.Sprintf("Database selectors %q are outside the authorized scope", databases),
	)
}

func RequireApproval(ctx context.Context, action, subjectSHA256 string) error {
	authorization, err := RequireAuthorization(ctx)
	if err != nil {
		return err
	}
	if authorization.Approval == nil {
		return securityError(result.CodePermissionDenied, "operation requires explicit approval")
	}
	approval := *authorization.Approval
	if err := approval.Validate(); err != nil {
		return err
	}
	if approval.Action != action || !strings.EqualFold(approval.SubjectSHA256, subjectSHA256) {
		return securityError(result.CodePermissionDenied, "approval does not match the requested action and subject")
	}
	return nil
}

func IsAuthorized(authorization Authorization, selectors ...string) bool {
	if authorization.Validate() != nil {
		return false
	}
	for _, selector := range selectors {
		found := false
		for _, allowed := range authorization.AuthorizedDatabases {
			if canonical(allowed) == canonical(selector) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func AllowsAnyDatabase(authorization Authorization, selectors ...string) bool {
	if authorization.Validate() != nil {
		return false
	}
	for _, selector := range selectors {
		for _, allowed := range authorization.AuthorizedDatabases {
			if canonical(allowed) == canonical(selector) {
				return true
			}
		}
	}
	return false
}

func ValidateMetadataText(value string, maximum int, required bool) error {
	if invalidText(value, maximum, required) {
		return securityError(result.CodeValidation, "untrusted metadata text is invalid or exceeds its budget")
	}
	return nil
}

func invalidText(value string, maximum int, required bool) bool {
	if !utf8.ValidString(value) || len([]rune(value)) > maximum {
		return true
	}
	if required && strings.TrimSpace(value) == "" {
		return true
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func canonical(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func securityError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}
