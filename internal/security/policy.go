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
	AuthorizationVersion     = "memora.authorization/v2"
	ApprovalVersion          = "memora.approval/v1"
	ActionInstallPackage     = "INSTALL_PACKAGE"
	ActionApplyRouteMutation = "APPLY_ROUTE_MUTATION"
	ActionApplySchemaChange  = "APPLY_SCHEMA_CHANGE"
	maxDatabaseScope         = 32
)

type RiskLevel string

const (
	LevelRead         RiskLevel = "L0"
	LevelWrite        RiskLevel = "L1"
	LevelStructural   RiskLevel = "L2"
	LevelIrreversible RiskLevel = "L3"
)

type Authorization struct {
	Version             string               `json:"version"`
	Actor               string               `json:"actor"`
	AuthorizedDatabases []string             `json:"authorized_databases"`
	DefaultLevel        RiskLevel            `json:"default_level,omitempty"`
	DatabaseLevels      map[string]RiskLevel `json:"database_levels,omitempty"`
	Approval            *Approval            `json:"approval,omitempty"`
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
	if _, valid := levelRank(authorization.effectiveDefaultLevel()); !valid {
		return securityError(result.CodeValidation, "Authorization default level is invalid")
	}
	levelKeys := map[string]bool{}
	for database, level := range authorization.DatabaseLevels {
		normalized := canonical(database)
		if normalized == "" || levelKeys[normalized] || !seen[normalized] {
			return securityError(result.CodeValidation, "Authorization Database level override is invalid")
		}
		if _, valid := levelRank(level); !valid {
			return securityError(result.CodeValidation, "Authorization Database level is invalid")
		}
		levelKeys[normalized] = true
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
	return RequireDatabaseLevel(ctx, LevelRead, database)
}

func RequireAnyDatabase(ctx context.Context, databases ...string) error {
	return RequireAnyDatabaseLevel(ctx, LevelRead, databases...)
}

func RequireDatabaseLevel(ctx context.Context, level RiskLevel, database string) error {
	return RequireAnyDatabaseLevel(ctx, level, database)
}

func RequireAnyDatabaseLevel(ctx context.Context, level RiskLevel, databases ...string) error {
	authorization, err := RequireAuthorization(ctx)
	if err != nil {
		return err
	}
	if AllowsAnyDatabaseLevel(authorization, level, databases...) {
		return nil
	}
	return securityError(
		result.CodePermissionDenied,
		fmt.Sprintf("Database selectors %q do not permit risk level %s", databases, level),
	)
}

func RequireLevel(ctx context.Context, level RiskLevel) error {
	authorization, err := RequireAuthorization(ctx)
	if err != nil {
		return err
	}
	granted, _ := levelRank(authorization.effectiveDefaultLevel())
	required, valid := levelRank(level)
	if valid && granted >= required {
		return nil
	}
	return securityError(result.CodePermissionDenied, fmt.Sprintf("Authorization does not permit risk level %s", level))
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
	return AllowsAnyDatabaseLevel(authorization, LevelRead, selectors...)
}

func AllowsAnyDatabaseLevel(authorization Authorization, level RiskLevel, selectors ...string) bool {
	if authorization.Validate() != nil {
		return false
	}
	required, valid := levelRank(level)
	if !valid {
		return false
	}
	for _, selector := range selectors {
		for _, allowed := range authorization.AuthorizedDatabases {
			if canonical(allowed) == canonical(selector) {
				granted, _ := levelRank(authorization.levelForDatabase(allowed))
				return granted >= required
			}
		}
	}
	return false
}

func (authorization Authorization) effectiveDefaultLevel() RiskLevel {
	if authorization.DefaultLevel == "" {
		return LevelWrite
	}
	return authorization.DefaultLevel
}

func (authorization Authorization) levelForDatabase(database string) RiskLevel {
	for selector, level := range authorization.DatabaseLevels {
		if canonical(selector) == canonical(database) {
			return level
		}
	}
	return authorization.effectiveDefaultLevel()
}

func levelRank(level RiskLevel) (int, bool) {
	switch level {
	case LevelRead:
		return 0, true
	case LevelWrite:
		return 1, true
	case LevelStructural:
		return 2, true
	case LevelIrreversible:
		return 3, true
	default:
		return 0, false
	}
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
