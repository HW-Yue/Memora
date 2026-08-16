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
	if manifest.Version != BundleVersion || len(manifest.Assets) != 12 {
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
	if !strings.Contains(text, `src="/assets/app.js?v=3"`) || !strings.Contains(text, `href="/assets/app.css?v=3"`) {
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

func TestAdminShellUsesMinimalNavigationAndPresentation(t *testing.T) {
	t.Parallel()

	index, err := fs.ReadFile(embeddedFiles, "dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	indexText := string(index)
	for _, forbidden := range []string{
		"LOCAL SEMANTIC DATABASE", "你的知识，由 AI 建模", "nav-caption",
		`data-nav="overview"`, `data-nav="routes"`, "Admin Shell 已离线加载",
	} {
		if strings.Contains(indexText, forbidden) {
			t.Errorf("minimal Admin shell still contains %q", forbidden)
		}
	}
	app, err := fs.ReadFile(embeddedFiles, "dist/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	appText := string(app)
	if !strings.Contains(appText, `window.history.replaceState(null, "", "/catalog")`) {
		t.Fatal("root Admin route does not redirect to Catalog")
	}
	for _, asset := range []string{
		"dist/assets/catalog.js", "dist/assets/changes.js", "dist/assets/diffs.js",
		"dist/assets/rows.js", "dist/assets/routes.js", "dist/assets/traces.js",
	} {
		content, readErr := fs.ReadFile(embeddedFiles, asset)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(content), "snapshot ${") {
			t.Errorf("%s still renders internal snapshot labels", asset)
		}
	}
}

func TestRouteTraceViewModuleUsesScopedBoundedParameterizedMSQL(t *testing.T) {
	t.Parallel()

	index, err := fs.ReadFile(embeddedFiles, "dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `href="/traces" data-route data-nav="traces"`) {
		t.Fatal("Admin shell does not expose Route Traces navigation")
	}
	app, err := fs.ReadFile(embeddedFiles, "dist/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), `from "./traces.js?v=3"`) ||
		!strings.Contains(string(app), `path === "/traces"`) ||
		!strings.Contains(string(app), `path.startsWith("/traces/")`) {
		t.Fatal("Admin shell does not route the Route Trace module")
	}
	traces, err := fs.ReadFile(embeddedFiles, "dist/assets/traces.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(traces)
	for _, required := range []string{
		"SHOW DATABASES LIMIT 32 COMPACT", "DESCRIBE DATABASE", "SHOW ROUTE TRACES IN DATABASE",
		"CURSOR :cursor LIMIT 20", "SHOW ROUTE TRACE :trace IN DATABASE", "LIMIT 24",
		"CURSOR :cursor LIMIT 24", "parameters", "named", "trace_id", "trace_sequence",
		"database_id", "anti_scope", "row.anti_scope !== undefined", "table_id",
		"candidate_route_ids", "selected_route_id", "locators",
		"elapsed_ms", "remaining_budget", "loading", "empty", "ready", "truncated",
		"permission", "corrupt", "revision_conflict",
	} {
		if !strings.Contains(javascript, required) {
			t.Errorf("Route Trace module is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"innerHTML", "SELECT ", "SHOW HISTORY", "SHOW CHANGE", "INSERT ", "UPDATE ",
		"DELETE ", "CREATE ", "prompt", "reasoning", "row_detail", "localStorage", "sessionStorage",
	} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("Route Trace module contains forbidden %q", forbidden)
		}
	}
}

func TestRowRevisionDiffModuleUsesTwoBoundedParameterizedAsOfPointReads(t *testing.T) {
	t.Parallel()

	app, err := fs.ReadFile(embeddedFiles, "dist/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), `from "./diffs.js?v=3"`) ||
		!strings.Contains(string(app), `path.startsWith("/diffs/")`) {
		t.Fatal("Admin shell does not route the Row revision diff module")
	}
	rows, err := fs.ReadFile(embeddedFiles, "dist/assets/rows.js")
	if err != nil {
		t.Fatal(err)
	}
	changes, err := fs.ReadFile(embeddedFiles, "dist/assets/changes.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rows), "/diffs/${encodeURIComponent") ||
		!strings.Contains(string(changes), "/diffs/${encodeURIComponent") {
		t.Fatal("History and Row change entries do not link to revision diff")
	}
	diffs, err := fs.ReadFile(embeddedFiles, "dist/assets/diffs.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(diffs)
	for _, required := range []string{
		"SELECT * FROM", "AS OF REVISION :before", "AS OF REVISION :after",
		"WHERE row_id = :row LIMIT 1", "parameters", "named", "row_detail",
		"memora.row-detail/v1", "column_id", "semantic_role", "TextEncoder",
		"MAX_BODY_BYTES", "loading", "ready", "empty", "permission", "corrupt", "over_budget",
	} {
		if !strings.Contains(javascript, required) {
			t.Errorf("Row revision diff module is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"innerHTML", "SHOW HISTORY", "SHOW CHANGE", "DESCRIBE ROUTE", "INSERT ", "UPDATE ",
		"DELETE ", "RESTORE ", "CREATE ", "localStorage", "sessionStorage",
	} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("Row revision diff module contains forbidden %q", forbidden)
		}
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
	if !strings.Contains(string(app), `from "./changes.js?v=3"`) ||
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
		"database_ids", "anti_scope", "row.anti_scope !== undefined", "entry_count", "object_kind",
		"history_locator", "related_object_ids",
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
	if !strings.Contains(string(app), `from "./rows.js?v=3"`) ||
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
	if strings.Contains(string(index), `data-nav="routes"`) {
		t.Fatal("Route Tree should be entered from a Table, not exposed as an empty top-level page")
	}
	app, err := fs.ReadFile(embeddedFiles, "dist/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), `from "./routes.js?v=3"`) ||
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
		"DESCRIBE ROUTE :route", "SHOW ROUTES UNDER :route", "OPEN ROUTE :route LIMIT 1",
		"CURSOR :cursor LIMIT 12", "Route leaf must contain at most one locator", "parameters", "named",
		"loading", "empty", "ready", "truncated", "permission", "corrupt", "revision_conflict",
		"database_id", "table_id", "row_id", "revision",
	} {
		if !strings.Contains(javascript, required) {
			t.Errorf("Route Tree module is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"INSERT ", "UPDATE ", "DELETE ", "CREATE ",
		"localStorage", "sessionStorage", "OPEN ROUTE :route CURSOR",
	} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("Route Tree module contains forbidden %q", forbidden)
		}
	}
}

func TestRouteTreeModuleValidatesVersionedAliasesContract(t *testing.T) {
	t.Parallel()

	routes, err := fs.ReadFile(embeddedFiles, "dist/assets/routes.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(routes)
	for _, required := range []string{
		`"route_id", "database_id", "table_id", "parent_id", "path", "name", "aliases", "kind", "purpose", "revision"`,
		"function validateAliases", "Array.from(alias).length", "new TextEncoder().encode(alias).length",
		"Route aliases are invalid", "Route aliases are duplicated", "Route aliases exceed their byte budget",
	} {
		if !strings.Contains(javascript, required) {
			t.Errorf("Route Tree aliases contract is missing %q", required)
		}
	}
}

func TestRouteTreeNeverPaginatesBranchOverflow(t *testing.T) {
	t.Parallel()

	routes, err := fs.ReadFile(embeddedFiles, "dist/assets/routes.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(routes)
	for _, forbidden := range []string{"继续加载这一层", "route_more_", "appendRootPage", "moreNode"} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("Route Tree still paginates with %q", forbidden)
		}
	}
}

func TestAdminSemanticCanvasBundleContract(t *testing.T) {
	t.Parallel()

	index, err := fs.ReadFile(embeddedFiles, "dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	indexText := string(index)
	for _, required := range []string{
		`/assets/vendor/g6-5.1.1.min.js?v=1`,
		`/assets/vendor/markdown-it-15.0.0.min.js?v=1`,
		`/assets/vendor/dompurify-3.4.7.min.js?v=1`,
	} {
		if !strings.Contains(indexText, required) {
			t.Errorf("Admin shell is missing local vendor %q", required)
		}
	}

	catalog, err := fs.ReadFile(embeddedFiles, "dist/assets/catalog.js")
	if err != nil {
		t.Fatal(err)
	}
	catalogText := string(catalog)
	for _, required := range []string{
		"查看表结构", "进入语义索引", "aria-label", "dataset.route",
	} {
		if !strings.Contains(catalogText, required) {
			t.Errorf("Catalog is missing semantic canvas navigation %q", required)
		}
	}

	routes, err := fs.ReadFile(embeddedFiles, "dist/assets/routes.js")
	if err != nil {
		t.Fatal(err)
	}
	routeText := string(routes)
	for _, required := range []string{
		"window.G6", "compact-box", "drag-canvas", "scroll-canvas", "collapse-expand",
		"OPEN ROUTE :route LIMIT 1", "SELECT * FROM", "MEMORA ROW", "documentNode",
		"聚焦到中心", "aria-label", "fitView", "kind === \"document\"",
		"DOCUMENT_NODE_WIDTH", "documentWidth", "translateElementTo", "focusElement",
		"type: (data) => data.kind === \"document\" ? \"html\" : \"rect\"",
		"innerHTML: documentNodeHTML", "markdownit({ html: false", "DOMPurify.sanitize",
		"semantic-document-node", "semantic-document-reading", "semantic-document-properties",
		"trackpad-pan", "trackpad-zoom", "event.ctrlKey", "event.metaKey",
		"zoomRange: [0.25, 2]", "sensitivity: 0.2",
		"autoFit: false", "animation: false,\n    zoomRange: [0.25, 2]", "node.childrenLoaded === true",
		"animation: false,\n        align: true",
		"requestAnimationFrame", "prefers-reduced-motion", "semantic-document-enter",
		"BRANCH_ENTER_MS", "DOCUMENT_ENTER_MS", "cancelAnimationFrame", "motionOpacity", "getNodeData",
		"graph.localMotion", "graph.draw()", "__semanticGraph", "localMotion?.cancel",
		"installCanvasGestureBridge", "pointerdown", "pointermove", "pointerup", "onWheel",
		"graph.translateBy", "graph.zoomBy", "deltaY", "caretPositionFromPoint", "setBaseAndExtent",
		"semantic-canvas-fullscreen", "semantic-canvas-controls", "返回表",
		"alignDocumentColumn", "DOCUMENT_COLUMN_GAP", "DOCUMENT_VERTICAL_GAP",
		"CANVAS_FOCUS_MAX_ZOOM", "zoomTo", "focusRouteNode",
		"for (const column of preview.columns)",
	} {
		if !strings.Contains(routeText, required) {
			t.Errorf("Semantic canvas is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"route-canvas-inspector", "canvas-inline-preview", "canvas-inline-close",
		"打开完整文档", "preview.columns.slice", "documentText", "labelWordWrap",
		"\"drag-canvas\", \"zoom-canvas\"", "placeDocumentAfterLeaf",
	} {
		if strings.Contains(routeText, forbidden) {
			t.Errorf("Semantic canvas still renders a floating DOM preview %q", forbidden)
		}
	}
	if strings.Contains(routeText, "const pending = statusDocumentNode") {
		t.Error("Leaf click still renders a temporary document before final Row content")
	}

	rows, err := fs.ReadFile(embeddedFiles, "dist/assets/rows.js")
	if err != nil {
		t.Fatal(err)
	}
	rowText := string(rows)
	for _, required := range []string{
		"markdownit", "html: false", "DOMPurify.sanitize", "DOMParser", "查看 Markdown 原文",
		"row-document-paper", "row-side-panel",
	} {
		if !strings.Contains(rowText, required) {
			t.Errorf("Row document view is missing %q", required)
		}
	}

	styles, err := fs.ReadFile(embeddedFiles, "dist/assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	styleText := string(styles)
	for _, required := range []string{
		".route-rows .content { width: 100%; max-width: none;",
		".route-rows .route-outlet { max-width: none; padding: 0; border: 0; background: transparent;",
		"grid-template-columns: minmax(0, 920px) minmax(240px, 290px)",
		".semantic-document-node", "width: 900px", ".semantic-document-reading",
		".semantic-document-metadata", ".semantic-document-properties",
		"user-select: none", "touch-action: none", "overscroll-behavior: contain",
		"@keyframes semantic-document-enter", "prefers-reduced-motion: reduce",
		".route-semantic-detail .topbar", ".route-semantic-detail .sidebar",
		".route-semantic-detail .semantic-canvas-stage", ".semantic-canvas-controls",
		"route-semantic-detail",
	} {
		if !strings.Contains(styleText, required) {
			t.Errorf("Wide Row document layout is missing %q", required)
		}
	}
	for _, forbidden := range []string{".canvas-inline-preview", ".canvas-inline-close", ".route-canvas-inspector"} {
		if strings.Contains(styleText, forbidden) {
			t.Errorf("Semantic canvas stylesheet still contains floating preview %q", forbidden)
		}
	}

	app, err := fs.ReadFile(embeddedFiles, "dist/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), "window.scrollTo({ top: 0") {
		t.Error("Admin client-side navigation does not reset the document viewport")
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
	if !strings.Contains(string(app), `from "./catalog.js?v=3"`) ||
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
		"/changes/db_work", "/diffs/db_work/tbl_notes/row_1/1/2",
		"/traces/db_work/tbl_notes/trace_00000000000000000000000000000001",
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
		"/assets/changes.js", "/assets/diffs.js", "/assets/traces.js",
	} {
		response := httptest.NewRecorder()
		bundle.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Header().Get("ETag") == "" ||
			response.Header().Get("Cache-Control") != "no-store, no-cache, must-revalidate, proxy-revalidate" ||
			response.Header().Get("Pragma") != "no-cache" ||
			response.Header().Get("Expires") != "0" {
			t.Fatalf("GET %s status=%d headers=%#v", path, response.Code, response.Header())
		}
		conditional := httptest.NewRequest(http.MethodGet, path, nil)
		conditional.Header.Set("If-None-Match", response.Header().Get("ETag"))
		notModified := httptest.NewRecorder()
		bundle.ServeHTTP(notModified, conditional)
		if notModified.Code != http.StatusNotModified {
			t.Fatalf("conditional GET %s status=%d", path, notModified.Code)
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
