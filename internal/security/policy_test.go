package security_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

func TestAuthorizationBoundsDatabaseScopeAndApprovalToPackageHash(t *testing.T) {
	t.Parallel()

	authorization := security.Authorization{
		Version:             security.AuthorizationVersion,
		Actor:               "agent:host",
		AuthorizedDatabases: []string{"work", "db_personal"},
		Approval: &security.Approval{
			Version:       security.ApprovalVersion,
			Action:        security.ActionInstallPackage,
			SubjectSHA256: strings.Repeat("a", 64),
			Confirmed:     true,
		},
	}
	ctx := security.WithAuthorization(context.Background(), authorization)
	if err := security.RequireDatabase(ctx, "WORK"); err != nil {
		t.Fatal(err)
	}
	if err := security.RequireDatabase(ctx, "private"); code(err) != result.CodePermissionDenied {
		t.Fatalf("RequireDatabase() error = %v", err)
	}
	if err := security.RequireAnyDatabase(ctx, "private", "db_personal"); err != nil {
		t.Fatalf("RequireAnyDatabase() error = %v", err)
	}
	if err := security.RequireApproval(ctx, security.ActionInstallPackage, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := security.RequireApproval(ctx, security.ActionInstallPackage, strings.Repeat("b", 64)); code(err) != result.CodePermissionDenied {
		t.Fatalf("RequireApproval() error = %v", err)
	}
}

func TestAuthorizationRejectsEmptyDuplicateAndOversizedScope(t *testing.T) {
	t.Parallel()

	cases := []security.Authorization{
		{Version: security.AuthorizationVersion, Actor: "agent:host"},
		{Version: security.AuthorizationVersion, Actor: "agent:host", AuthorizedDatabases: []string{"work", " WORK "}},
		{Version: "future", Actor: "agent:host", AuthorizedDatabases: []string{"work"}},
	}
	tooMany := security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:host",
		AuthorizedDatabases: make([]string, 33),
	}
	for index := range tooMany.AuthorizedDatabases {
		tooMany.AuthorizedDatabases[index] = "db-" + strings.Repeat("x", index+1)
	}
	cases = append(cases, tooMany)
	for _, authorization := range cases {
		if err := authorization.Validate(); err == nil {
			t.Fatalf("Validate() accepted %#v", authorization)
		}
	}
}

func TestAuthorizationV2EnforcesDefaultAndPerDatabaseLevels(t *testing.T) {
	t.Parallel()

	authorization := security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:host",
		AuthorizedDatabases: []string{"work", "archive"},
		DefaultLevel:        security.LevelRead,
		DatabaseLevels: map[string]security.RiskLevel{
			"work": security.LevelWrite,
		},
	}
	ctx := security.WithAuthorization(context.Background(), authorization)
	if err := security.RequireDatabaseLevel(ctx, security.LevelWrite, "WORK"); err != nil {
		t.Fatal(err)
	}
	if err := security.RequireDatabaseLevel(ctx, security.LevelWrite, "archive"); code(err) != result.CodePermissionDenied {
		t.Fatalf("archive L1 error = %v", err)
	}
	if err := security.RequireDatabaseLevel(ctx, security.LevelStructural, "work"); code(err) != result.CodePermissionDenied {
		t.Fatalf("work L2 error = %v", err)
	}
}

func TestAuthorizationV2RejectsInvalidLevelOverrides(t *testing.T) {
	t.Parallel()

	for _, authorization := range []security.Authorization{
		{
			Version: security.AuthorizationVersion, Actor: "agent:host",
			AuthorizedDatabases: []string{"work"}, DefaultLevel: "L9",
		},
		{
			Version: security.AuthorizationVersion, Actor: "agent:host",
			AuthorizedDatabases: []string{"work"},
			DatabaseLevels:      map[string]security.RiskLevel{"private": security.LevelRead},
		},
		{
			Version: security.AuthorizationVersion, Actor: "agent:host",
			AuthorizedDatabases: []string{"work"},
			DatabaseLevels:      map[string]security.RiskLevel{"work": security.LevelRead, " WORK ": security.LevelWrite},
		},
	} {
		if err := authorization.Validate(); err == nil {
			t.Fatalf("Validate() accepted %#v", authorization)
		}
	}
}

func code(err error) result.Code {
	var stable interface{ StableCode() string }
	if errors.As(err, &stable) {
		return result.Code(stable.StableCode())
	}
	return ""
}
