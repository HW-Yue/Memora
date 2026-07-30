package executor_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/wikiexport"
)

func TestWikiExportRunsThroughMSQLWithBoundProfile(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "vault")
	exporter := &fakeWikiExporter{}
	engine := executor.NewWithManagement(nil, nil, nil, exporter)
	document, err := parser.Parse(`EXPORT WIKI TO :path PROFILE :profile`)
	if err != nil {
		t.Fatal(err)
	}
	profileJSON := `{"version":"memora.wiki-profile/v1","tables":{}}`
	ctx := security.WithAuthorization(context.Background(), security.Authorization{
		Version: security.AuthorizationVersion, Actor: "user:test",
		AuthorizedDatabases: []string{"work"},
	})
	output, err := engine.Execute(ctx, document.Statement, executor.Parameters{Named: map[string]any{
		"path": root, "profile": profileJSON,
	}}, executor.MutationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if exporter.root != root || exporter.profile.Version != wikiexport.ProfileVersion {
		t.Fatalf("export call = %q, %#v", exporter.root, exporter.profile)
	}
	if len(exporter.databases) != 1 || exporter.databases[0] != "work" {
		t.Fatalf("export scope = %#v", exporter.databases)
	}
	if len(output.Rows) != 1 || output.Rows[0]["snapshot_sha256"] != "snapshot" ||
		output.Rows[0]["object_count"] != int64(2) || output.Rows[0]["path"] != root {
		t.Fatalf("output = %#v", output)
	}
}

func TestWikiExportRejectsRelativePathsAndUnknownProfileFields(t *testing.T) {
	t.Parallel()

	engine := executor.NewWithManagement(nil, nil, nil, &fakeWikiExporter{})
	for _, parameters := range []executor.Parameters{
		{Named: map[string]any{"path": "relative", "profile": `{"version":"memora.wiki-profile/v1","tables":{}}`}},
		{Named: map[string]any{"path": filepath.Join(t.TempDir(), "vault"), "profile": `{"version":"memora.wiki-profile/v1","tables":{},"code":"run"}`}},
	} {
		document, err := parser.Parse(`EXPORT WIKI TO :path PROFILE :profile`)
		if err != nil {
			t.Fatal(err)
		}
		ctx := security.WithAuthorization(context.Background(), security.Authorization{
			Version: security.AuthorizationVersion, Actor: "user:test",
			AuthorizedDatabases: []string{"work"},
		})
		if _, err := engine.Execute(ctx, document.Statement, parameters, executor.MutationOptions{}); err == nil {
			t.Fatalf("Execute() accepted %#v", parameters)
		}
	}
}

type fakeWikiExporter struct {
	root      string
	profile   wikiexport.Profile
	databases []string
}

func (exporter *fakeWikiExporter) Export(
	_ context.Context,
	root string,
	profile wikiexport.Profile,
	databases []string,
) (wikiexport.Manifest, error) {
	exporter.root, exporter.profile = root, profile
	exporter.databases = append([]string{}, databases...)
	return wikiexport.Manifest{
		Version: wikiexport.Version, SnapshotSHA256: "snapshot", ProfileSHA256: "profile",
		Objects: []wikiexport.Object{{RowID: "one"}, {RowID: "two"}},
	}, nil
}

func TestWikiExportRequiresExplicitDatabaseScope(t *testing.T) {
	t.Parallel()

	engine := executor.NewWithManagement(nil, nil, nil, &fakeWikiExporter{})
	document, err := parser.Parse(`EXPORT WIKI TO :path PROFILE :profile`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Execute(context.Background(), document.Statement, executor.Parameters{Named: map[string]any{
		"path":    filepath.Join(t.TempDir(), "vault"),
		"profile": `{"version":"memora.wiki-profile/v1","tables":{}}`,
	}}, executor.MutationOptions{})
	if code(err) != "permission_denied" {
		t.Fatalf("Execute() error = %v", err)
	}
}

func code(err error) string {
	if stable, ok := err.(interface{ StableCode() string }); ok {
		return stable.StableCode()
	}
	return ""
}
