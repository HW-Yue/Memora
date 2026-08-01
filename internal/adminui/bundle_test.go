package adminui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedBundleHasFrozenOfflineAssets(t *testing.T) {
	t.Parallel()

	bundle, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	manifest := bundle.Manifest()
	if manifest.Version != BundleVersion || len(manifest.Assets) != 7 {
		t.Fatalf("manifest = %#v", manifest)
	}
	for _, asset := range manifest.Assets {
		if len(asset.SHA256) != 64 || asset.Size == 0 || asset.ContentType == "" {
			t.Fatalf("asset = %#v", asset)
		}
	}
	index, err := fs.ReadFile(embeddedFiles, "dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(index)
	for _, forbidden := range []string{"https://", "http://", "<style", "localStorage", "sessionStorage"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("index contains forbidden %q", forbidden)
		}
	}
	if !strings.Contains(text, `src="/assets/app.js"`) || !strings.Contains(text, `href="/assets/app.css"`) {
		t.Fatalf("index does not use embedded assets: %s", text)
	}
	script, err := fs.ReadFile(embeddedFiles, "dist/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(script)
	for _, forbidden := range []string{"https://", "http://", "localStorage", "sessionStorage", "document.cookie", "console."} {
		if strings.Contains(javascript, forbidden) {
			t.Fatalf("JavaScript contains forbidden %q", forbidden)
		}
	}
	clearAt := strings.Index(javascript, "clearFragment();")
	fetchAt := strings.Index(javascript, `fetch("/api/v1/session"`)
	if clearAt < 0 || fetchAt < 0 || clearAt >= fetchAt || !strings.Contains(javascript, "export async function executeMSQL") {
		t.Fatalf("JavaScript does not clear fragment before bootstrap or expose the module API client")
	}
}

func TestChangeTimelineModuleUsesScopedBoundedParameterizedMSQL(t *testing.T) {
	t.Parallel()

	index, err := fs.ReadFile(embeddedFiles, "dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `href="/changes" data-route data-nav="changes"`) {
		t.Fatal("Admin shell does not expose Changes navigation")
	}
	app, err := fs.ReadFile(embeddedFiles, "dist/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), `from "./changes.js"`) ||
		!strings.Contains(string(app), `path === "/changes"`) ||
		!strings.Contains(string(app), `path.startsWith("/changes/")`) {
		t.Fatal("Admin shell does not route the Change timeline module")
	}
	changes, err := fs.ReadFile(embeddedFiles, "dist/assets/changes.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(changes)
	for _, required := range []string{
		"SHOW DATABASES LIMIT 32 COMPACT", "DESCRIBE DATABASE", "SHOW CHANGES IN DATABASE",
		"CURSOR :cursor LIMIT 20", "SHOW CHANGE :transaction IN DATABASE", "LIMIT 32",
		"CURSOR :cursor LIMIT 32", "parameters", "named", "transaction_id", "commit_sequence",
		"database_ids", "entry_count", "object_kind", "history_locator", "related_object_ids",
		"loading", "empty", "ready", "truncated", "permission", "corrupt", "revision_conflict",
	} {
		if !strings.Contains(javascript, required) {
			t.Errorf("Change timeline module is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"innerHTML", "SELECT ", "SHOW HISTORY", " AS OF ", "INSERT ", "UPDATE ", "DELETE ",
		"CREATE ", "localStorage", "sessionStorage",
	} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("Change timeline module contains forbidden %q", forbidden)
		}
	}
}

func TestRowDocumentModuleUsesDictionaryMetadataAndBoundedParameterizedMSQL(t *testing.T) {
	t.Parallel()

	app, err := fs.ReadFile(embeddedFiles, "dist/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), `from "./rows.js"`) ||
		!strings.Contains(string(app), `path.startsWith("/rows/")`) {
		t.Fatal("Admin shell does not route the Row document module")
	}
	routes, err := fs.ReadFile(embeddedFiles, "dist/assets/routes.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(routes), "/rows/${encodeURIComponent") {
		t.Fatal("Route locator does not link to its Row document")
	}
	rows, err := fs.ReadFile(embeddedFiles, "dist/assets/rows.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(rows)
	for _, required := range []string{
		"SELECT * FROM", "WHERE row_id = :row LIMIT 1", "SHOW HISTORY FROM",
		"FOR ROW :row LIMIT 20", "CURSOR :cursor LIMIT 20", "parameters", "named",
		"row_detail", "memora.row-detail/v1", "semantic_role", "title_column",
		"summary_column", "row_id_revision", "column_id", "purpose",
		"loading", "empty", "ready", "truncated", "permission", "corrupt", "revision_conflict",
	} {
		if !strings.Contains(javascript, required) {
			t.Errorf("Row document module is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"innerHTML", " AS OF ", "INSERT ", "UPDATE ", "DELETE ", "CREATE ",
		"localStorage", "sessionStorage",
	} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("Row document module contains forbidden %q", forbidden)
		}
	}
}

func TestRouteTreeModuleUsesBoundedParameterizedMSQLAndDefinesEveryPageState(t *testing.T) {
	t.Parallel()

	index, err := fs.ReadFile(embeddedFiles, "dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `href="/routes" data-route`) {
		t.Fatal("Admin shell does not expose Route Tree navigation")
	}
	app, err := fs.ReadFile(embeddedFiles, "dist/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), `from "./routes.js"`) ||
		!strings.Contains(string(app), `path.startsWith("/routes/")`) {
		t.Fatal("Admin shell does not route the Route Tree module")
	}
	catalog, err := fs.ReadFile(embeddedFiles, "dist/assets/catalog.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(catalog), "/routes/${encodeURIComponent") {
		t.Fatal("Catalog Table does not link to its Route Tree")
	}
	routes, err := fs.ReadFile(embeddedFiles, "dist/assets/routes.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(routes)
	for _, required := range []string{
		"DESCRIBE TABLE", "SHOW ROUTES FROM TABLE", "AT ROOT LIMIT 12",
		"DESCRIBE ROUTE :route", "SHOW ROUTES UNDER :route", "OPEN ROUTE :route",
		"CURSOR :cursor LIMIT 12", "CURSOR :cursor LIMIT 20", "parameters", "named",
		"loading", "empty", "ready", "truncated", "permission", "corrupt", "revision_conflict",
		"database_id", "table_id", "row_id", "revision",
	} {
		if !strings.Contains(javascript, required) {
			t.Errorf("Route Tree module is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"innerHTML", "SELECT ", "INSERT ", "UPDATE ", "DELETE ", "CREATE ",
		"localStorage", "sessionStorage",
	} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("Route Tree module contains forbidden %q", forbidden)
		}
	}
}

func TestCatalogModuleUsesBoundedStableIDMSQLAndDefinesEveryPageState(t *testing.T) {
	t.Parallel()

	index, err := fs.ReadFile(embeddedFiles, "dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `href="/catalog" data-route`) {
		t.Fatal("Admin shell does not expose stable Catalog navigation")
	}
	app, err := fs.ReadFile(embeddedFiles, "dist/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), `from "./catalog.js"`) ||
		!strings.Contains(string(app), "popstate") {
		t.Fatal("Admin shell does not route Catalog or browser history")
	}
	catalog, err := fs.ReadFile(embeddedFiles, "dist/assets/catalog.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(catalog)
	for _, required := range []string{
		"SHOW DATABASES LIMIT 32 COMPACT",
		"DESCRIBE DATABASE",
		"SHOW TABLES FROM",
		"DESCRIBE TABLE",
		"SHOW COLUMNS FROM",
		"CURSOR :cursor LIMIT 32 COMPACT",
		"loading", "empty", "ready", "truncated", "permission", "corrupt", "revision_conflict",
		`replaceAll('"', '""')`,
	} {
		if !strings.Contains(javascript, required) {
			t.Errorf("Catalog module is missing %q", required)
		}
	}
	for _, forbidden := range []string{"innerHTML", "INSERT ", "UPDATE ", "DELETE ", "CREATE ", "localStorage", "sessionStorage"} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("Catalog module contains forbidden %q", forbidden)
		}
	}
}

func TestBundleServesDeepLinksAssetsAndSecurityHeaders(t *testing.T) {
	t.Parallel()

	bundle, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/", "/catalog/work", "/routes/db_work/tbl_notes/route_1", "/rows/db_work/tbl_notes/row_1",
		"/changes/db_work",
	} {
		response := httptest.NewRecorder()
		bundle.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Memora Admin") {
			t.Fatalf("GET %s status=%d body=%q", path, response.Code, response.Body.String())
		}
		if response.Header().Get("Content-Security-Policy") == "" ||
			response.Header().Get("Referrer-Policy") != "no-referrer" ||
			response.Header().Get("X-Frame-Options") != "DENY" ||
			response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s headers = %#v", path, response.Header())
		}
	}
	for _, path := range []string{
		"/assets/app.js", "/assets/app.css", "/assets/catalog.js", "/assets/routes.js", "/assets/rows.js",
		"/assets/changes.js",
	} {
		response := httptest.NewRecorder()
		bundle.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Header().Get("ETag") == "" ||
			response.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatalf("GET %s status=%d headers=%#v", path, response.Code, response.Header())
		}
	}
	for _, path := range []string{"/assets/missing.js", "/api/v1/unknown"} {
		response := httptest.NewRecorder()
		bundle.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "Memora Admin") {
			t.Fatalf("GET %s status=%d body=%q", path, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	bundle.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/catalog", strings.NewReader("{}")))
	if response.Code != http.StatusMethodNotAllowed || strings.Contains(response.Body.String(), "Memora Admin") {
		t.Fatalf("POST deep link status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestBundleRejectsMissingExtraAndTamperedFiles(t *testing.T) {
	t.Parallel()

	files := copyEmbeddedFiles(t)
	delete(files, "dist/assets/app.js")
	if _, err := New(files); err == nil {
		t.Fatal("bundle missing JavaScript succeeded")
	}
	files = copyEmbeddedFiles(t)
	files["dist/extra.txt"] = &fstest.MapFile{Data: []byte("extra")}
	if _, err := New(files); err == nil {
		t.Fatal("bundle with extra asset succeeded")
	}
	files = copyEmbeddedFiles(t)
	files["dist/index.html"].Data = append(files["dist/index.html"].Data, []byte("tampered")...)
	if _, err := New(files); err == nil {
		t.Fatal("tampered bundle succeeded")
	}
}

func copyEmbeddedFiles(t *testing.T) fstest.MapFS {
	t.Helper()
	files := fstest.MapFS{}
	err := fs.WalkDir(embeddedFiles, "dist", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := fs.ReadFile(embeddedFiles, path)
		if err != nil {
			return err
		}
		files[path] = &fstest.MapFile{Data: append([]byte(nil), content...), Mode: 0o444}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
