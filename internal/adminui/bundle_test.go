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
	if manifest.Version != BundleVersion || len(manifest.Assets) != 13 {
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
	if !strings.Contains(text, `src="/assets/app.js?v=4"`) || !strings.Contains(text, `href="/assets/app.css?v=4"`) {
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
	if !strings.Contains(string(app), `from "./traces.js?v=4"`) ||
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
		"SHOW DATABASES LIMIT 32", "DESCRIBE DATABASE", "SHOW ROUTE TRACES IN DATABASE",
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
	if !strings.Contains(string(app), `from "./diffs.js?v=4"`) ||
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
		// The heading names the Row; the engine's row-semantics sentence is not
		// a description of it and was the same line on every revision.
		"before.detail.row_semantics",
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
	if !strings.Contains(string(app), `from "./changes.js?v=4"`) ||
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
		"SHOW DATABASES LIMIT 32", "DESCRIBE DATABASE", "SHOW CHANGES IN DATABASE",
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
	if !strings.Contains(string(app), `from "./rows.js?v=4"`) ||
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

func TestRouteTreeModuleUsesParameterizedMSQLAndDefinesEveryPageState(t *testing.T) {
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
	if !strings.Contains(string(app), `from "./routes.js?v=4"`) ||
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
		"DESCRIBE TABLE", "SHOW ROUTES FROM TABLE", "AT ROOT",
		"DESCRIBE ROUTE :route", "SHOW ROUTES UNDER :route", "OPEN ROUTE :route LIMIT 1",
		"Route leaf must contain at most one locator", "parameters", "named",
		// Admin 是展示层：后端有多少节点就画多少，一层能有几个是 Agent 的判断
		// （route_policy.branch_fanout），不是前端的限制。SHOW ROUTES 现在一次返回
		// 整层，所以页大小与 cursor 都不再经过这个模块。
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
		// The tree is rooted in a Table, and the root's sentence is that
		// Table's purpose — not the engine's row semantics, which says the same
		// thing under every Table.
		"purpose: table.row_semantics",
	} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("Route Tree module contains forbidden %q", forbidden)
		}
	}
	if !strings.Contains(javascript, "purpose: table.purpose") {
		t.Error("Route Tree root does not carry the Table's own purpose")
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

// SHOW ROUTES answers with the whole layer: a layer is bounded by
// route_policy.branch_fanout (≤100 children), the engine never truncates, and
// `truncated` is simply false. So the module sends each listing statement once,
// with no LIMIT and no CURSOR, and there is no next_cursor to follow.
func TestRouteTreeNeverPaginatesBranchOverflow(t *testing.T) {
	t.Parallel()

	routes, err := fs.ReadFile(embeddedFiles, "dist/assets/routes.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(routes)
	for _, required := range []string{
		"SHOW ROUTES FROM TABLE ${subject} AT ROOT`",
		`"SHOW ROUTES UNDER :route"`,
	} {
		if !strings.Contains(javascript, required) {
			t.Errorf("Route Tree does not ask for a whole layer: missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"继续加载这一层", "route_more_", "appendRootPage", "moreNode",
		// The retired paging parameters and the cursor loop that followed them.
		// A bare `next_cursor` stays in the module for OPEN ROUTE, which still
		// pages its locators; SHOW ROUTES itself must carry neither.
		"AT ROOT LIMIT", "AT ROOT CURSOR", "SHOW ROUTES UNDER :route LIMIT",
		"SHOW ROUTES UNDER :route CURSOR", "CURSOR :cursor", "routeChildrenBudget",
		"drainPages", "fetchPage", "rowsOf", "drained",
		"while (page.truncated)", "Route Tree cursor did not advance",
	} {
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
		"window.G6", "collapse-expand",
		"OPEN ROUTE :route LIMIT 1", "SELECT * FROM", "MEMORA ROW", "documentNode",
		"聚焦到中心", "aria-label", "fitView", "kind === \"document\"",
		"DOCUMENT_NODE_WIDTH", "documentWidth", "focusElement",
		"type: (data) => data.kind === \"document\" ? \"html\" : \"rect\"",
		"innerHTML: documentNodeHTML", "markdownit({ html: false", "DOMPurify.sanitize",
		"semantic-document-node", "semantic-document-reading",
		"event.ctrlKey", "event.metaKey",
		// 滚轮与触控板只有一条路径（本桥），并且带 rAF 兜底：G6 的 scroll-canvas
		// 曾经在同一个位置拖几次后卡住，而两套实现会按“鼠标停在哪”分工。
		"gestureTimer", "画布上的滚轮/触控板手势全部由这里处理",
		// 空白处的鼠标拖拽也必须平移：G6 的 drag-canvas 实测对空白处无效，所以
		// 画布上任何左键按下都走这条桥，只有浮层控件除外。
		"isCanvasControl", "画布空白处也要能拖",
		// 空白处按下不能立刻抢指针：节点就画在空白处，抢了指针 G6 就收不到点击，
		// 展开/收起会整体失效。先记起点，超过阈值才算拖动。
		"CANVAS_DRAG_THRESHOLD", "pendingPress", "event.pointerType === \"touch\"",
		// 丢掉 pointerup 之后画布会一直跟着鼠标跑：没按键的移动必须结束这次拖动。
		"event.buttons === 0", "abandonGesture",
		"zoomRange: [0.25, 2]",
		"autoFit: false", "animation: false,\n    zoomRange: [0.25, 2]", "node.childrenLoaded === true",
		"animation: false,\n        align: true",
		"requestAnimationFrame", "semantic-document-enter",
		"DOCUMENT_ENTER_MS", "cancelAnimationFrame", "getNodeData",
		"height > 0 ? height : DOCUMENT_NODE_MIN_HEIGHT",
		"__semanticGraph",
		// A Route name is prose, so the box is measured and the label wraps; the
		// layout reads the same numbers, which is what keeps edges on the boxes.
		"routeNodeLayout", "labelWordWrap: true", "labelMaxLines:", "labelTextOverflow:",
		"labelWordWrapWidth:",
		// The tree layout is handed `{id, data: node.data || {}}` by G6's
		// treeLayout while this project's node data is flat, so a size callback
		// that reads the node it is given reads `{}` — which is how a document
		// card, a thousand pixels tall, was laid out as a 220x72 Route box and
		// every edge became a long diagonal. The size the layout gets is looked up
		// by node id in a registry rebuilt from the same tree as the graph data.
		"rememberLayoutSizes", "layoutNodeSize", "antv-dagre",
		// Trackpad input is coalesced into one transform per frame; the per-frame
		// fade that redrew the whole canvas is gone with it.
		"requestAnimationFrame(applyGesture)", "pendingPan", "pendingZoom", "flushGesture",
		"installCanvasGestureBridge", "pointerdown", "pointermove", "pointerup", "onWheel",
		"graph.translateBy", "graph.zoomBy", "deltaY", "caretPositionFromPoint", "setBaseAndExtent",
		"semantic-canvas-fullscreen", "semantic-canvas-controls", "返回库",
		// The back control names the library it returns to, so the canvas reads
		// DESCRIBE DATABASE in the same batch as the table and its routes: a label
		// that names a place the link does not go is worse than no label at all.
		"DESCRIBE DATABASE ", "data.database.name",
		"CANVAS_FOCUS_MAX_ZOOM", "zoomTo", "focusRouteNode",
		"for (const column of preview.columns)",
	} {
		if !strings.Contains(routeText, required) {
			t.Errorf("Semantic canvas is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"route-canvas-inspector", "canvas-inline-preview", "canvas-inline-close",
		"打开完整文档", "preview.columns.slice", "documentText",
		"\"drag-canvas\", \"zoom-canvas\"", "placeDocumentAfterLeaf",
		// The fade used to call updateNodeData + await graph.draw() every frame for
		// 140ms, which re-rendered every HTML node on each of those frames.
		"localMotion", "animateNodes", "motionOpacity", "BRANCH_ENTER_MS",
		// The behavior that was supposed to pan the background and did not.
		"\"drag-canvas\",", "type: \"scroll-canvas\"", "type: \"zoom-canvas\"",
		"trackpad-pan", "trackpad-zoom", "sensitivity: 0.2", "type: \"scroll-canvas\"", "type: \"zoom-canvas\"",
		"trackpad-pan", "trackpad-zoom", "sensitivity: 0.2",
		// The document column was a second vertical layout that did not know the
		// tree layout existed: it stacked every card by its real height and then
		// moved the whole thing to line its mean up with the old one. It shipped
		// the symptom rather than the fix — cards were placed by one coordinate
		// system while the layout that owns the rows used another — so it does not
		// come back, and neither does the `translateElementTo` pass that moved
		// cards after G6 had already drawn their edges.
		"alignDocumentColumn", "DOCUMENT_COLUMN_GAP", "DOCUMENT_VERTICAL_GAP",
		"translateElementTo",
		// Reading the size out of the layout's own node argument is the bug, not
		// a style choice: G6 hands the layout `{id, data}` with an empty `data`.
		"layoutNodeData", "isDocumentLayoutNode", "graphNodeWidth", "graphNodeHeight",
		"layoutNodeWidth", "layoutNodeHeight",
		// compact-box positions a node by the top-left corner of a box inflated by
		// its gaps while G6 draws the node's centre there, so it is only correct
		// while every node has one size. With a 900x527 card next to a 220x72 Route
		// box it draws the card over its Route at the wrong height; dagre takes a
		// size per node and answers with centres.
		"compact-box", "getHGap", "getVGap", "getWidth: layoutNode", "getHeight: layoutNode",
		// The canvas is a Table's Route tree, but the control returns to the
		// Database it belongs to; calling that "返回表" sent the reader to one page
		// while naming another.
		"返回表", "返回 ${data.object.name} 表",
	} {
		if strings.Contains(routeText, forbidden) {
			t.Errorf("Semantic canvas still carries %q", forbidden)
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
		"row-document-paper", "row-side-panel", "documentSection", "summary_column",
	} {
		if !strings.Contains(rowText, required) {
			t.Errorf("Row document view is missing %q", required)
		}
	}
	// A Row is a title and a document: the Record-fields area and every
	// per-Column field block are gone, because the engine refuses any Column
	// beyond the shape it owns. Nothing may quietly reintroduce them.
	for _, forbidden := range []string{"记录字段", "semantic-document-properties", "fieldSection", "row-summary"} {
		if strings.Contains(rowText, forbidden) {
			t.Errorf("Row document view still renders %q", forbidden)
		}
	}

	styles, err := fs.ReadFile(embeddedFiles, "dist/assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	styleText := string(styles)
	// A document card is as tall as its document. A fixed minimum left a hole
	// under every shorter Row, which is exactly what the reader sees.
	if strings.Contains(styleText, "min-height: 620px") || strings.Contains(styleText, "min-height: 430px") {
		t.Error("the document card still reserves height the document does not use")
	}
	for _, required := range []string{
		".route-rows .content { width: 100%; max-width: none;",
		".route-rows .route-outlet { max-width: none; padding: 0; border: 0; background: transparent;",
		"grid-template-columns: minmax(0, 920px) minmax(240px, 290px)",
		".semantic-document-node", "width: 900px", ".semantic-document-reading",
		".semantic-document-metadata", ".row-value",
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
	// Panning and zooming re-rasterize the card subtree, so the card may not ask
	// for a 72px-blur shadow or a permanent compositor layer per document.
	if !strings.Contains(styleText, "contain: paint") {
		t.Error("Document cards do not contain their paint")
	}
	for _, forbidden := range []string{"box-shadow: 0 28px 72px", "will-change: transform, opacity"} {
		if strings.Contains(styleText, forbidden) {
			t.Errorf("Document cards still pay for %q on every camera change", forbidden)
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
	if !strings.Contains(string(app), `from "./catalog.js?v=4"`) ||
		!strings.Contains(string(app), "popstate") {
		t.Fatal("Admin shell does not route Catalog or browser history")
	}
	catalog, err := fs.ReadFile(embeddedFiles, "dist/assets/catalog.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(catalog)
	for _, required := range []string{
		"SHOW DATABASES LIMIT 32",
		"DESCRIBE DATABASE",
		"SHOW TABLES FROM",
		"DESCRIBE TABLE",
		"SHOW COLUMNS FROM",
		"CURSOR :cursor LIMIT 32",
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
	// A Table card says what that Table holds. `row_semantics` is the engine's
	// statement about what a *Row* is — the same sentence in every Table — so
	// showing it as the Table's description put one piece of boilerplate on
	// every card and hid the purpose that was written for each one.
	if !strings.Contains(javascript, `element("p", "", row.purpose)`) {
		t.Error("Catalog module does not describe a Table with its own purpose")
	}
	for _, forbidden := range []string{"row_semantics || row.purpose", "? row.row_semantics"} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("Catalog module still prints the engine's row semantics as a Table description (%q)", forbidden)
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

// The search page is a read-only MSQL client like the rest of the Admin: it may
// recall positions, it may not read facts. Phase one runs the keyword arm only —
// the vector arm needs a query embedding, and the Admin deliberately has no
// Provider — so the test pins both what it does and what it must not pretend to
// do yet.
func TestSearchViewModuleRecallsPositionsAndLinksIntoTheTree(t *testing.T) {
	t.Parallel()

	index, err := fs.ReadFile(embeddedFiles, "dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `href="/search" data-route data-nav="search"`) {
		t.Fatal("Admin shell does not expose the Search navigation")
	}
	app, err := fs.ReadFile(embeddedFiles, "dist/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	appText := string(app)
	for _, required := range []string{
		`from "./search.js?v=5"`, `path === "/search"`, `path.startsWith("/search/")`,
		"export async function searchDatabase", `fetch("/api/v1/search"`,
	} {
		if !strings.Contains(appText, required) {
			t.Errorf("Admin shell does not route the Search module: missing %q", required)
		}
	}

	search, err := fs.ReadFile(embeddedFiles, "dist/assets/search.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(search)
	for _, required := range []string{
		"SHOW DATABASES LIMIT", "COMPACT", "SHOW TABLES FROM",
		"memora.admin-search/v1", "vector.ran", "vectorNote",
		"not_configured", "embedding_timeout", "provider_unavailable", "vector_not_ready",
		"route_id", "database_id", "table_id", "object_id",
		"truncated", "revision_conflict", "permission_denied",
		"loading", "empty", "ready",
		"/routes/", "dataset.route", "encodeURIComponent",
	} {
		if !strings.Contains(javascript, required) {
			t.Errorf("Search module is missing %q", required)
		}
	}
	// The page does not talk MSQL for recall any more: the engine fuses within a
	// Database, and the page only interleaves across Databases (which have no
	// common rank to fuse) — a second per-arm ordering here would be a second
	// opinion about relevance.
	if !strings.Contains(javascript, "跨 Database 才是交错") {
		t.Error("Search module must say that only the cross-Database listing is interleaved")
	}
	for _, forbidden := range []string{"RECALL FROM", "MATCH :query", "NEAREST :"} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("Search module still does %q", forbidden)
		}
	}
	// The keyword index is a pair stream, so two characters is a word and one is
	// not. The minimum lives in three places at once — the input's own
	// minLength, the submit guard, and the one message that explains a refusal —
	// and a browser bubble reading "3 个字符或更多" is what a stale minLength
	// looks like to the reader.
	if !strings.Contains(javascript, "input.minLength = 2") {
		t.Error("Search input still enforces a minimum the index does not have")
	}
	if !strings.Contains(javascript, "至少 2 个字符") {
		t.Error("Search module does not state the two-character minimum")
	}
	// No how-to-write-it copy: the box is named for screen readers and says
	// nothing about how to phrase a query.
	for _, forbidden := range []string{"placeholder", "用一句话", "3 个字符"} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("Search module still carries instruction copy (%q)", forbidden)
		}
	}
	if !strings.Contains(javascript, `setAttribute("aria-label"`) {
		t.Error("Search input has no accessible name now that it has no placeholder")
	}
	// The badge on a card is that position's own provenance — which arms found it,
	// never how strong the match was. It used to be one label per Database
	// ("两臂融合（RRF）" whenever that Database's vector arm had run), which told
	// the reader that a neighbour only the vector arm returned had been found by
	// both arms.
	for _, required := range []string{"armLabel", "row.arms", "仅向量", "仅关键词", "关键词+向量"} {
		if !strings.Contains(javascript, required) {
			t.Errorf("Search module does not label a position with its own arms: missing %q", required)
		}
	}
	for _, forbidden := range []string{`receipt.vector.ran ? "两臂融合`, "hit.source"} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("Search module still labels the whole Database instead of the position (%q)", forbidden)
		}
	}
	for _, forbidden := range []string{
		"innerHTML", "SELECT ", "SHOW HISTORY", "SHOW CHANGE", "INSERT ", "UPDATE ",
		"DELETE ", "CREATE ", "localStorage", "sessionStorage", "console.",
		"NEAREST", "innerHTML",
	} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("Search module contains forbidden %q", forbidden)
		}
	}

	// A deep link into an unloaded branch has to expand its ancestors rather than
	// tell the reader to walk the tree by hand.
	routes, err := fs.ReadFile(embeddedFiles, "dist/assets/routes.js")
	if err != nil {
		t.Fatal(err)
	}
	routesText := string(routes)
	for _, required := range []string{"expandToRoute", "DESCRIBE ROUTE :route", "focusElement"} {
		if !strings.Contains(routesText, required) {
			t.Errorf("route tree view does not expand a deep link: missing %q", required)
		}
	}
}
