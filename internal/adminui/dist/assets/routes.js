// Admin 是展示层，对一层能显示多少个节点不设任何上限：后端有多少就画多少。
// 节点数量该不该增长是 Agent 的判断（route_policy.branch_fanout，F223），不是这里的事。
//
// SHOW ROUTES 一次返回整层：服务端从不截断（一层最多 branch_fanout 个子节点，≤100），
// 所以这两条 SHOW ROUTES 既不传 LIMIT/CURSOR，也没有 page/next_cursor/snapshot 可跟。
// 展示层自己截断就等于把"这一层有多少个"这个 Agent 的判断换成了前端的。
const LOCATOR_LIMIT = 1;
const DOCUMENT_ENTER_MS = 180;

class RouteViewError extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
  }
}

function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = String(text);
  return node;
}

function stableID(value, prefix, label) {
  if (typeof value !== "string" || value.length > 200 || !value.startsWith(prefix) ||
      !/^[A-Za-z0-9_-]+$/.test(value)) {
    throw new RouteViewError("corrupt", `${label} stable ID is invalid`);
  }
  return value;
}

function quoteIdentifier(value, prefix) {
  stableID(value, prefix, "Catalog");
  return `"${value.replaceAll('"', '""')}"`;
}

function statementInput(named) {
  return { parameters: { named } };
}

function pathParts(path) {
  const raw = path.split("/").filter(Boolean);
  if (raw[0] !== "routes" || ![1, 3, 4].includes(raw.length)) {
    throw new RouteViewError("corrupt", "Route Tree path is invalid");
  }
  try {
    return raw.slice(1).map(decodeURIComponent);
  } catch (_) {
    throw new RouteViewError("corrupt", "Route Tree path encoding is invalid");
  }
}

function resultsFrom(envelope, count) {
  if (!envelope || envelope.version !== "memora.result/v1" || !Array.isArray(envelope.results)) {
    throw new RouteViewError("corrupt", "Result envelope version is invalid");
  }
  if (!envelope.ok) {
    let failure = envelope.error;
    if (!failure) failure = envelope.results.find((item) => item && item.error)?.error;
    throw new RouteViewError(failure?.code || "corrupt", "Route Tree request failed");
  }
  if (envelope.results.length !== count || envelope.results.some((result) =>
    !result || result.status !== "succeeded" || !Array.isArray(result.rows))) {
    throw new RouteViewError("corrupt", "Route Tree result shape is invalid");
  }
  return envelope.results;
}

// OPEN ROUTE 仍然分页（它不在"整层返回"里），所以这一个封套还是要验；SHOW ROUTES 已不再用它。
function validatePage(result, limit) {
  const page = result.page;
  if (!page || page.version !== "memora.list-page/v1" || page.limit !== limit ||
      typeof page.cursor !== "string" || !/^sha256:[a-f0-9]{64}$/.test(page.snapshot) ||
      typeof page.truncated !== "boolean" ||
      (page.truncated && (typeof page.next_cursor !== "string" || page.next_cursor.length === 0))) {
    throw new RouteViewError("corrupt", "Route Tree page contract is invalid");
  }
  return page;
}

function exactKeys(row, expected) {
  return Object.keys(row).sort().join("\u0000") === [...expected].sort().join("\u0000");
}

function positiveRevision(value) {
  return Number.isSafeInteger(value) && value > 0;
}

function validateAliases(value, name) {
  if (!Array.isArray(value) || value.length > 8) {
    throw new RouteViewError("corrupt", "Route aliases are invalid");
  }
  const aliases = new Set();
  let totalBytes = 0;
  for (const alias of value) {
    if (typeof alias !== "string" || alias.length === 0 || alias !== alias.trim() ||
        Array.from(alias).length > 64) {
      throw new RouteViewError("corrupt", "Route aliases are invalid");
    }
    const key = alias.toLowerCase();
    if (key === name.toLowerCase() || aliases.has(key)) {
      throw new RouteViewError("corrupt", "Route aliases are duplicated");
    }
    aliases.add(key);
    totalBytes += new TextEncoder().encode(alias).length;
  }
  if (totalBytes > 512) {
    throw new RouteViewError("corrupt", "Route aliases exceed their byte budget");
  }
  return value;
}

function tablePoint(result, databaseID, tableID) {
  if (result.rows.length !== 1) throw new RouteViewError("corrupt", "Table point result is invalid");
  const row = result.rows[0];
  if (!row || row.database_id !== databaseID || row.table_id !== tableID ||
      typeof row.name !== "string" || row.name.length === 0 ||
      typeof row.purpose !== "string" || row.purpose.length === 0 ||
      typeof row.row_semantics !== "string" || row.row_semantics.length === 0 ||
      !positiveRevision(row.schema_version)) {
    throw new RouteViewError("corrupt", "Table scope or fields are invalid");
  }
  return row;
}

function validateRouteRow(row, point, databaseID = "", tableID = "", parentID = "") {
  // The listing carries the Row a leaf holds; `DESCRIBE ROUTE` does not, so the
  // point's key set is the node's plus the synopsis rather than the listing's.
  // Keeping the two apart is the whole value of the exact-key check: a surface
  // that grew a field without this list is refused instead of drawn wrong.
  const nodeKeys = ["route_id", "database_id", "table_id", "parent_id", "path", "name", "aliases", "kind", "purpose", "revision"];
  const childKeys = [...nodeKeys, "row_id", "row_revision"];
  const pointKeys = [...nodeKeys, "synopsis"];
  if (!row || !exactKeys(row, point ? pointKeys : childKeys)) {
    throw new RouteViewError("corrupt", "Route node fields are invalid");
  }
  stableID(row.route_id, "route_", "Route");
  if (row.parent_id !== "") stableID(row.parent_id, "route_", "Route parent");
  if (typeof row.path !== "string" || !row.path.startsWith("/") ||
      typeof row.name !== "string" || row.name.length === 0 ||
      typeof row.purpose !== "string" || row.purpose.length === 0 ||
      !["root", "branch", "leaf"].includes(row.kind) || !positiveRevision(row.revision)) {
    throw new RouteViewError("corrupt", "Route node values are invalid");
  }
  // A Row binding belongs to a leaf and arrives as a pair or not at all: the
  // engine reports null on a root, a branch, and on a leaf with nothing live
  // under it. Half a binding is a broken contract, not a missing value — and the
  // binding is the fact's handle, so it may not be drawn unchecked.
  if (!point) {
    const bound = row.row_id !== null || row.row_revision !== null;
    if (bound) {
      if (row.kind !== "leaf") throw new RouteViewError("corrupt", "Route Row binding is on a non-leaf");
      stableID(row.row_id, "row_", "Route Row");
      if (!positiveRevision(row.row_revision)) {
        throw new RouteViewError("corrupt", "Route Row revision is invalid");
      }
    }
  }
  validateAliases(row.aliases, row.name);
  if (point) {
    if (row.database_id !== databaseID || row.table_id !== tableID || typeof row.synopsis !== "string") {
      throw new RouteViewError("corrupt", "Route point scope is invalid");
    }
  } else if (row.kind === "root" || row.parent_id === "" || (parentID && row.parent_id !== parentID)) {
    throw new RouteViewError("corrupt", "Route child scope is invalid");
  }
  return row;
}

function routeRows(result, databaseID, tableID, parentID = "") {
  const rows = result.rows.map((row) => validateRouteRow(row, false, databaseID, tableID, parentID));
  const ids = new Set();
  const parents = new Set();
  for (const row of rows) {
    if (ids.has(row.route_id)) throw new RouteViewError("corrupt", "Route page has duplicate nodes");
    ids.add(row.route_id);
    parents.add(row.parent_id);
  }
  if (!parentID && parents.size > 1) throw new RouteViewError("corrupt", "Route roots disagree on parent");
  return rows;
}

function locatorRows(result, databaseID, tableID) {
  const expected = ["database_id", "table_id", "row_id", "revision"];
  const ids = new Set();
  for (const row of result.rows) {
    if (!row || !exactKeys(row, expected) || row.database_id !== databaseID || row.table_id !== tableID ||
        !positiveRevision(row.revision)) {
      throw new RouteViewError("corrupt", "Route locator scope is invalid");
    }
    stableID(row.database_id, "db_", "Locator Database");
    stableID(row.table_id, "tbl_", "Locator Table");
    stableID(row.row_id, "row_", "Locator Row");
    if (ids.has(row.row_id)) throw new RouteViewError("corrupt", "Route page has duplicate locators");
    ids.add(row.row_id);
  }
  return result.rows;
}

function stateNode(kind, title, detail) {
  const state = element("div", "view-state");
  state.dataset.kind = kind;
  state.append(element("strong", "", title), element("p", "", detail));
  return state;
}

function showState(root, kind, title, detail) {
  root.dataset.pageState = kind;
  root.replaceChildren(stateNode(kind, title, detail));
}

function errorState(error) {
  if (error.code === "permission_denied") {
    return ["permission", "无权查看这棵 Route Tree", "当前 Admin session 未授权该 Database。"];
  }
  if (error.code === "revision_conflict") {
    return ["error", "Route Tree 已发生变化", "读到的快照已经不是现在这棵树（这一层被改过），刷新后重新读。"];
  }
  if (error.code === "corrupt" || error.code === "internal_error" || error.code === "constraint_violation") {
    return ["corrupt", "Route Tree 响应无法验证", "页面拒绝展示不完整或跨 scope 的语义索引。"];
  }
  return ["error", "暂时无法读取 Route Tree", "请确认 daemon 正常运行后重试。"];
}

function breadcrumbs(parts) {
  const node = element("nav", "breadcrumbs");
  node.setAttribute("aria-label", "Route Tree 路径");
  const root = element("a", "", "Route Tree");
  root.href = "/routes";
  root.dataset.route = "";
  node.append(root);
  for (const part of parts) {
    node.append(element("span", "", "/"));
    const item = part.href ? element("a", "", part.label) : element("span", "", part.label);
    if (part.href) {
      item.href = part.href;
      item.dataset.route = "";
    }
    node.append(item);
  }
  return node;
}

function heading(title, purpose) {
  const wrapper = element("header", "catalog-heading route-heading");
  const content = element("div");
  content.append(element("h2", "", title));
  if (purpose) content.append(element("p", "", purpose));
  wrapper.append(content);
  return wrapper;
}

function routeCard(row, databaseID, tableID) {
  const card = element("a", "route-node");
  card.href = `/routes/${encodeURIComponent(databaseID)}/${encodeURIComponent(tableID)}/${encodeURIComponent(row.route_id)}`;
  card.dataset.route = "";
  const marker = element("span", `route-kind route-kind-${row.kind}`, row.kind);
  const text = element("div");
  text.append(element("strong", "", row.name), element("p", "", row.purpose));
  card.append(marker, text);
  return card;
}

function locatorCard(row) {
  const card = element("a", "locator-card");
  card.href = `/rows/${encodeURIComponent(row.database_id)}/${encodeURIComponent(row.table_id)}/${encodeURIComponent(row.row_id)}`;
  card.dataset.route = "";
  card.append(element("strong", "", "Row"), element("span", "", `revision ${row.revision}`));
  return card;
}

// The canvas's back control returns to the Database this Table belongs to, so it
// is named after the Database and not after the Table. That name is the only
// reason this view reads a Database at all, and it comes in the same statement
// batch as the Table and its Routes: the page must not be able to draw a control
// naming one place while linking to another.
function databasePoint(result, databaseID) {
  if (result.rows.length !== 1) throw new RouteViewError("corrupt", "Database point result is invalid");
  const row = result.rows[0];
  if (!row || row.database_id !== databaseID ||
      typeof row.name !== "string" || row.name.length === 0 ||
      typeof row.purpose !== "string" || row.purpose.length === 0 ||
      typeof row.scope !== "string" || row.scope.length === 0 ||
      !positiveRevision(row.schema_version)) {
    throw new RouteViewError("corrupt", "Database scope or fields are invalid");
  }
  return row;
}

async function loadTableRoot(executeMSQL, databaseID, tableID) {
  const database = quoteIdentifier(databaseID, "db_");
  const subject = `${database}.${quoteIdentifier(tableID, "tbl_")}`;
  const source =
    `DESCRIBE DATABASE ${database} COMPACT; DESCRIBE TABLE ${subject} COMPACT; ` +
    `SHOW ROUTES FROM TABLE ${subject} AT ROOT`;
  const results = resultsFrom(await executeMSQL(source, [
    statementInput({}), statementInput({}), statementInput({})
  ]), 3);
  return {
    database: databasePoint(results[0], databaseID),
    object: tablePoint(results[1], databaseID, tableID),
    rows: routeRows(results[2], databaseID, tableID),
  };
}

async function describeRoute(executeMSQL, databaseID, tableID, routeID) {
  stableID(routeID, "route_", "Route");
  const result = resultsFrom(await executeMSQL("DESCRIBE ROUTE :route", [statementInput({ route: routeID })]), 1)[0];
  if (result.rows.length !== 1) throw new RouteViewError("corrupt", "Route point result is invalid");
  return validateRouteRow(result.rows[0], true, databaseID, tableID);
}

async function loadChildren(executeMSQL, databaseID, tableID, routeID) {
  const result = resultsFrom(await executeMSQL(
    "SHOW ROUTES UNDER :route", [statementInput({ route: routeID })]
  ), 1)[0];
  return { rows: routeRows(result, databaseID, tableID, routeID) };
}

async function loadLocators(executeMSQL, databaseID, tableID, routeID) {
  const source = "OPEN ROUTE :route LIMIT 1";
  const named = { route: routeID };
  const result = resultsFrom(await executeMSQL(source, [statementInput(named)]), 1)[0];
  const data = {
    rows: locatorRows(result, databaseID, tableID),
    page: validatePage(result, LOCATOR_LIMIT)
  };
  if (data.rows.length > 1 || data.page.truncated || data.page.next_cursor) {
    throw new RouteViewError("corrupt", "Route leaf must contain at most one locator");
  }
  return data;
}

// A Route name is Agent-written prose. 220px held "Admin 搜索页" and let
// "Admin 显示槽位：文档居中且只渲染一次" run out of its box and across the
// neighbouring node, so the box is measured from the same CSS font the canvas
// draws with and the label wraps to at most two lines. The layout reads the same
// numbers (getWidth/getHeight below), which is what keeps edges on the boxes.
const ROUTE_NODE_MIN_WIDTH = 220;
const ROUTE_NODE_MAX_WIDTH = 360;
const ROUTE_NODE_HEIGHT = 72;
const ROUTE_NODE_LINE_HEIGHT = 22;
const ROUTE_NODE_PADDING = 16;
const ROUTE_NODE_MAX_LINES = 2;
const ROUTE_LABEL_FONT = '700 14px Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif';
const DOCUMENT_NODE_WIDTH = 900;
// A measured height sets the card; this is only the floor for the degenerate case
// where measurement returns nothing (a hidden or not-yet-laid-out node). It used
// to be a fixed 620px, which left a hole under every shorter document.
const DOCUMENT_NODE_MIN_HEIGHT = 120;
const CANVAS_FOCUS_MAX_ZOOM = 1.25;
// 空白处的一次按下既可能是"拖画布"，也可能是"点节点"。移动没超过这个像素数就按点击放行。
const CANVAS_DRAG_THRESHOLD = 3;
const SYSTEM_COLUMNS = ["row_id", "revision", "commit_sequence", "row_state", "schema_version"];

function displayValue(value) {
  if (value === null) return "null";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  const encoded = JSON.stringify(value, null, 2);
  if (encoded === undefined) throw new RouteViewError("corrupt", "Row value cannot be displayed");
  return encoded;
}

async function loadRowPreview(executeMSQL, locator) {
  const subject = `${quoteIdentifier(locator.database_id, "db_")}.${quoteIdentifier(locator.table_id, "tbl_")}`;
  const source = `SELECT * FROM ${subject} WHERE row_id = :row LIMIT 1`;
  const results = resultsFrom(await executeMSQL(source, [statementInput({ row: locator.row_id })]), 1);
  const result = results[0];
  if (!Array.isArray(result.columns) || !result.row_detail || result.rows.length > 1) {
    throw new RouteViewError("corrupt", "Row preview result is invalid");
  }
  const row = result.rows[0];
  if (!row) return { row: null, columns: result.columns, detail: result.row_detail };
  if (row.row_id !== locator.row_id || !positiveRevision(row.revision) || row.row_state !== "live") {
    throw new RouteViewError("corrupt", "Row preview identity is invalid");
  }
  return { row, columns: result.columns, detail: result.row_detail };
}

function markdownFragment(value) {
  if (typeof value !== "string" || typeof window.markdownit !== "function" ||
      !window.DOMPurify || typeof window.DOMPurify.sanitize !== "function") {
    return null;
  }
  const markdownit = window.markdownit({ html: false, linkify: false, typographer: false });
  const clean = window.DOMPurify.sanitize(markdownit.render(value), { USE_PROFILES: { html: true } });
  const parsed = new DOMParser().parseFromString(clean, "text/html");
  const fragment = document.createDocumentFragment();
  for (const child of parsed.body.childNodes) fragment.append(child.cloneNode(true));
  return fragment;
}

function appendMarkdown(parent, value) {
  const rendered = element("div", "markdown-body semantic-document-reading");
  const fragment = markdownFragment(value);
  if (fragment) rendered.append(fragment);
  else rendered.append(element("pre", "semantic-document-plain", displayValue(value)));
  parent.append(rendered);
}

function documentTitle(locator, preview) {
  const titleColumn = preview.detail.display.title_column;
  if (preview.row && titleColumn && preview.row[titleColumn] !== null) {
    return displayValue(preview.row[titleColumn]);
  }
  return `${locator.row_id} · revision ${locator.revision}`;
}

function documentNodeElement(locator, preview, state) {
  const article = element("article", `semantic-document-node semantic-document-${state}`);
  const finish = () => {
    const motion = element("div", "semantic-document-motion semantic-document-enter");
    motion.style.animationDuration = `${DOCUMENT_ENTER_MS}ms`;
    while (article.firstChild) motion.append(article.firstChild);
    article.append(motion);
    return article;
  };
  const header = element("header", "semantic-document-header");
  header.append(element("p", "semantic-document-kicker", "MEMORA ROW"));

  if (state !== "ready") {
    const lines = String(preview).split("\n");
    header.append(element("h2", "", lines.shift() || "Row 内容"));
    const body = element("div", "semantic-document-status");
    body.append(element("p", "", lines.join("\n")));
    article.append(header, body);
    return finish();
  }

  if (!preview.row) {
    header.append(element("h2", "", "当前 Row 不可见"));
    const body = element("div", "semantic-document-status");
    body.append(element("p", "", "locator 仍存在，但当前 revision 没有 live value。"));
    article.append(header, body);
    return finish();
  }

  header.append(element("h2", "", documentTitle(locator, preview)));

  const metadata = element("dl", "semantic-document-metadata");
  for (const column of preview.columns) {
    if (!SYSTEM_COLUMNS.includes(column.name)) continue;
    const item = element("div", "semantic-document-meta-item");
    item.append(element("dt", "", column.name),
      element("dd", "", displayValue(preview.row[column.name])));
    metadata.append(item);
  }
  header.append(metadata);
  article.append(header);

  const titleColumn = preview.detail.display.title_column || "";
  const summaryColumn = preview.detail.display.summary_column || "";
  const body = element("section", "semantic-document-body");
  if (summaryColumn) appendMarkdown(body, preview.row[summaryColumn]);
  else body.append(element("p", "semantic-document-empty", "这张表没有配置 summary 字段。"));
  article.append(body);

  // A Row is a title and a document. There is no third place for a field: the
  // engine refuses any Column beyond the two it owns, so an empty "record
  // fields" area would be permanent furniture rather than metadata.
  return finish();
}

function measuredDocumentHeight(article) {
  const probe = article.cloneNode(true);
  probe.classList.add("semantic-document-measure");
  document.body.append(probe);
  const height = Math.ceil(probe.getBoundingClientRect().height);
  probe.remove();
  // The document decides how tall its card is: a short document is a short card,
  // not a page with a hole under it. The floor is only for a measurement that
  // returned nothing at all.
  return height > 0 ? height : DOCUMENT_NODE_MIN_HEIGHT;
}

function documentNodeHTML(data) {
  return data.kind === "document" ? data.documentHTML : "";
}

function documentNode(locator, preview, state = "ready") {
  const article = documentNodeElement(locator, preview, state);
  return {
    id: `row_document_${locator.row_id}`,
    kind: "document",
    name: "MEMORA ROW",
    documentHTML: article.outerHTML,
    documentWidth: DOCUMENT_NODE_WIDTH,
    documentHeight: measuredDocumentHeight(article),
    documentState: state,
    children: [],
  };
}

function findDocumentNode(node) {
  return (node.children || []).find((child) => child.kind === "document") || null;
}

let routeLabelContext = null;

function routeLabelMetrics() {
  if (routeLabelContext === null) {
    routeLabelContext = document.createElement("canvas").getContext("2d");
  }
  if (routeLabelContext) routeLabelContext.font = ROUTE_LABEL_FONT;
  return routeLabelContext;
}

// routeNodeLayout wraps a name the way the canvas will wrap it (the font is the
// one in ROUTE_LABEL_FONT, one character at a time — Chinese has no spaces) and
// returns the box that fits the wrapped result.
function routeNodeLayout(name) {
  const text = String(name ?? "");
  const context = routeLabelMetrics();
  const available = ROUTE_NODE_MAX_WIDTH - ROUTE_NODE_PADDING * 2;
  if (!context || text === "") {
    return { width: ROUTE_NODE_MIN_WIDTH, height: ROUTE_NODE_HEIGHT, lines: 1, wrapWidth: available };
  }
  const lines = [];
  let line = "";
  for (const character of text) {
    const candidate = line + character;
    if (line !== "" && context.measureText(candidate).width > available) {
      lines.push(line);
      line = character;
      if (lines.length === ROUTE_NODE_MAX_LINES) break;
    } else {
      line = candidate;
    }
  }
  if (lines.length < ROUTE_NODE_MAX_LINES && line !== "") lines.push(line);
  const longest = lines.reduce((widest, current) =>
    Math.max(widest, context.measureText(current).width), 0);
  const width = Math.min(ROUTE_NODE_MAX_WIDTH,
    Math.max(ROUTE_NODE_MIN_WIDTH, Math.ceil(longest) + ROUTE_NODE_PADDING * 2));
  return {
    width,
    height: ROUTE_NODE_HEIGHT + (lines.length - 1) * ROUTE_NODE_LINE_HEIGHT,
    lines: lines.length,
    wrapWidth: width - ROUTE_NODE_PADDING * 2,
  };
}

function graphNodeSize(data) {
  if (data?.kind === "document") {
    return [data.documentWidth || DOCUMENT_NODE_WIDTH, data.documentHeight || DOCUMENT_NODE_MIN_HEIGHT];
  }
  const layout = routeNodeLayout(data?.name);
  return [layout.width, layout.height];
}

// G6's `treeLayout` hands the layout `{id: idOf(node), data: node.data || {}}`,
// and a node in this view is flat: `treeToGraphData` passes the object straight
// through, which is why the style callbacks can read `data.kind` off it. A flat
// object has no `.data`, so a size callback that reads the node it is given
// reads `{}` — every node, document cards included, was laid out as a 220x72
// Route box, and the layout never reserved the thousand pixels a card actually
// occupies. That is what turned every edge into a long diagonal.
//
// So the layout does not read the node: it reads this registry by id. The
// registry is rebuilt from the same tree the graph data was built from, in the
// same call, so the two can never describe different sizes. One canvas per page
// makes a module-level registry safe; the alternative — threading a map through
// every `setData` call site — buys nothing and can go stale on one of them.
const layoutNodeSizes = new Map();

function rememberLayoutSizes(tree) {
  layoutNodeSizes.clear();
  const remember = (node) => {
    const layout = routeNodeLayout(node?.name);
    layoutNodeSizes.set(node.id, node.kind === "document"
      ? [node.documentWidth || DOCUMENT_NODE_WIDTH, node.documentHeight || DOCUMENT_NODE_MIN_HEIGHT]
      : [layout.width, layout.height]);
    for (const child of node.children || []) remember(child);
  };
  remember(tree);
  return layoutNodeSizes;
}

function layoutNodeSize(node) {
  return layoutNodeSizes.get(node?.id) || [ROUTE_NODE_MIN_WIDTH, ROUTE_NODE_HEIGHT];
}

function treeNode(row) {
  return {
    id: row.route_id,
    route_id: row.route_id,
    parent_id: row.parent_id,
    path: row.path,
    name: row.name,
    aliases: row.aliases,
    kind: row.kind,
    purpose: row.purpose,
    revision: row.revision,
    children: [],
    childrenLoaded: false,
  };
}

function treeRoot(table, rows) {
  return {
    id: "table-root",
    name: table.name,
    kind: "root",
    purpose: table.purpose,
    children: rows.map(treeNode),
    childrenLoaded: true,
  };
}

function findTreeNode(node, id) {
  if (node.id === id) return node;
  for (const child of node.children || []) {
    const found = findTreeNode(child, id);
    if (found) return found;
  }
  return null;
}

function graphData(tree) {
  rememberLayoutSizes(tree);
  return window.G6.treeToGraphData(tree, {
    getNodeData: (node, depth) => {
      node.depth = depth;
      return node.children ?
        { ...node, children: node.children.map((child) => child.id) } : node;
    },
  });
}

function setCanvasState(stage, kind, title, detail) {
  const current = stage.querySelector(".canvas-state");
  if (current) current.remove();
  const state = stateNode(kind, title, detail);
  state.classList.add("canvas-state");
  stage.append(state);
}

function clearCanvasState(stage) {
  const current = stage.querySelector(".canvas-state");
  if (current) current.remove();
}

async function replaceDocumentNode(graph, tree, node, document) {
  node.children = document ? [document] : [];
  graph.setData(graphData(tree));
  await graph.render();
  return document;
}

async function appendChildren(graph, tree, node, executeMSQL, databaseID, tableID) {
  const next = await loadChildren(executeMSQL, databaseID, tableID, node.route_id);
  node.children = next.rows.map(treeNode);
  node.childrenLoaded = true;
  const selected = node.id;
  graph.setData(graphData(tree));
  await graph.render();
  if (graph.expandElement) await graph.expandElement(selected, { animation: false });
}

// expandToRoute walks the ancestor chain of a deep-linked node and expands each
// branch on the way down, so a link into an unloaded leaf lands where it says it
// does instead of asking the reader to expand the tree by hand. The chain comes
// from DESCRIBE ROUTE, one hop at a time, and the walk stops at the Table root.
async function expandToRoute(graph, tree, routeID, executeMSQL, databaseID, tableID) {
  const chain = [];
  let current = routeID;
  for (let hop = 0; hop < 64 && current; hop += 1) {
    const node = await describeRoute(executeMSQL, databaseID, tableID, current);
    chain.push(node);
    if (node.parent_id === "" || node.parent_id === node.route_id) break;
    current = node.parent_id;
  }
  chain.reverse();
  for (const node of chain) {
    const loaded = findTreeNode(tree, node.route_id);
    if (!loaded || loaded.kind !== "branch" || loaded.childrenLoaded) continue;
    await appendChildren(graph, tree, loaded, executeMSQL, databaseID, tableID);
  }
  return findTreeNode(tree, routeID);
}

// The floating controls sit above the canvas and are buttons, not canvas: a drag
// that starts on them must not move the view.
function isCanvasControl(target) {
  return target instanceof Element && !!target.closest(".semantic-canvas-controls");
}

function installCanvasGestureBridge(graph, container) {
  let pan = null;
  let pendingPress = null;
  let selecting = null;
  // A trackpad sends pointermove at up to 120Hz and a two-finger scroll sends a
  // stream of wheel events; calling translateBy/zoomBy for each one asks the
  // canvas to transform a DOM subtree more often than the screen refreshes. The
  // deltas are accumulated here and applied at most once per frame instead.
  let pendingPan = null;
  let pendingZoom = null;
  let gestureFrame = 0;
  let gestureTimer = 0;
  const applyGesture = () => {
    gestureFrame = 0;
    if (gestureTimer) {
      clearTimeout(gestureTimer);
      gestureTimer = 0;
    }
    if (!container.isConnected) {
      pendingPan = null;
      pendingZoom = null;
      return;
    }
    if (pendingPan) {
      const [dx, dy] = pendingPan;
      pendingPan = null;
      if (dx || dy) graph.translateBy?.([dx, dy], false);
    }
    if (pendingZoom) {
      const { ratio, x, y } = pendingZoom;
      pendingZoom = null;
      if (ratio !== 1) graph.zoomBy?.(ratio, false, [x, y]);
    }
  };
  const scheduleGesture = () => {
    if (!gestureFrame) gestureFrame = requestAnimationFrame(applyGesture);
    // rAF 在后台标签或极端掉帧时可能很晚才来，累积的位移就会一直躺着不动（同一个位置
    // 拖几次突然"卡住"）。兜底：最多 100ms 一定把它应用掉。
    if (!gestureTimer) {
      gestureTimer = setTimeout(() => {
        gestureTimer = 0;
        if (gestureFrame) cancelAnimationFrame(gestureFrame);
        applyGesture();
      }, 100);
    }
  };
  const flushGesture = () => {
    if (gestureFrame) cancelAnimationFrame(gestureFrame);
    applyGesture();
  };
  const caretAtPoint = (x, y) => {
    const position = document.caretPositionFromPoint?.(x, y);
    if (position) return { node: position.offsetNode, offset: position.offset };
    const range = document.caretRangeFromPoint?.(x, y);
    return range ? { node: range.startContainer, offset: range.startOffset } : null;
  };
  const cleanupSelection = () => {
    if (selecting) {
      selecting.element.classList.remove(
        "semantic-document-selecting", "semantic-document-selected");
    }
    selecting = null;
  };
  const onPointerDown = (event) => {
    if (event.button !== 0) return;
    const target = event.target instanceof Element ?
      event.target.closest(".semantic-document-node") : null;
    if (!target) {
      // 画布空白处也要能拖。这里原来直接 return，把空白处的平移交给了 G6 的
      // drag-canvas；实测它在真实鼠标下不动——在空白处按下、拖过 25 次 pointermove、
      // 松开，相机坐标一模一样，而同一手势落在卡片上或触控板滚轮上都有效。
      //
      // 但按下这一刻什么都不做：语义节点就画在空白处，一按下就 setPointerCapture 会把
      // pointerup/click 重定向到容器，G6 再也收不到节点点击，展开与收起会整体失效。
      // 所以先只记住起点，等 pointermove 超过阈值才真的开始平移（见 onPointerMove）。
      if (event.pointerType === "touch" || event.altKey || isCanvasControl(event.target)) return;
      if (!pan) pendingPress = { pointerID: event.pointerId, x: event.clientX, y: event.clientY };
      return;
    }
    if (event.altKey) {
      cleanupSelection();
      const caret = caretAtPoint(event.clientX, event.clientY);
      if (!caret || !target.contains(caret.node)) return;
      selecting = {
        element: target,
        pointerID: event.pointerId,
        anchorNode: caret.node,
        anchorOffset: caret.offset,
      };
      target.classList.add("semantic-document-selecting");
      container.setPointerCapture?.(event.pointerId);
      window.getSelection()?.setBaseAndExtent(
        caret.node, caret.offset, caret.node, caret.offset);
      event.preventDefault();
      event.stopPropagation();
      return;
    }
    cleanupSelection();
    window.getSelection()?.removeAllRanges();
    pan = { pointerID: event.pointerId, x: event.clientX, y: event.clientY };
    container.setPointerCapture?.(event.pointerId);
    event.preventDefault();
    event.stopPropagation();
  };
  const onPointerMove = (event) => {
    if (selecting && selecting.pointerID === event.pointerId) {
      const caret = caretAtPoint(event.clientX, event.clientY);
      if (caret && selecting.element.contains(caret.node)) {
        window.getSelection()?.setBaseAndExtent(
          selecting.anchorNode, selecting.anchorOffset, caret.node, caret.offset);
      }
      event.preventDefault();
      event.stopPropagation();
      return;
    }
    if (pan && pan.pointerID === event.pointerId && event.buttons === 0) {
      // 画布外的松开、丢掉的 pointerup、切走的应用，都能让 pan 留在原地；之后鼠标
      // 一动画布就跟着跑（用户看到的"鼠标到哪它到哪，退不出来"）。实测抓到过：15 次
      // pointerdown 只有 14 次 pointerup，而此后每一次 pointermove 都在"没按键"的
      // 情况下继续平移。所以只要有一次没按键的移动到达，就认定这次拖动结束了。
      pan = null;
    }
    if (pendingPress && pendingPress.pointerID === event.pointerId) {
      const movedX = event.clientX - pendingPress.x;
      const movedY = event.clientY - pendingPress.y;
      if (Math.abs(movedX) < CANVAS_DRAG_THRESHOLD && Math.abs(movedY) < CANVAS_DRAG_THRESHOLD) {
        return;
      }
      pan = { pointerID: event.pointerId, x: pendingPress.x, y: pendingPress.y };
      pendingPress = null;
      container.setPointerCapture?.(event.pointerId);
    }
    if (!pan || pan.pointerID !== event.pointerId) return;
    const dx = event.clientX - pan.x;
    const dy = event.clientY - pan.y;
    pan.x = event.clientX;
    pan.y = event.clientY;
    if (dx || dy) {
      pendingPan = [(pendingPan ? pendingPan[0] : 0) + dx, (pendingPan ? pendingPan[1] : 0) + dy];
      scheduleGesture();
    }
    event.preventDefault();
    event.stopPropagation();
  };
  const onPointerUp = (event) => {
    if (pendingPress && pendingPress.pointerID === event.pointerId) {
      // 没超过阈值：这是一次点击，不抢指针、不拦事件，交给 G6 的 click-select /
      // node:click / collapse-expand。
      pendingPress = null;
      return;
    }
    if (pan && pan.pointerID === event.pointerId) {
      container.releasePointerCapture?.(event.pointerId);
      pan = null;
      // The last movement must not wait for a frame that will never come.
      flushGesture();
      event.preventDefault();
      event.stopPropagation();
    }
    if (selecting && selecting.pointerID === event.pointerId) {
      container.releasePointerCapture?.(event.pointerId);
      selecting.pointerID = null;
      selecting.element.classList.remove("semantic-document-selecting");
      selecting.element.classList.add("semantic-document-selected");
      event.preventDefault();
      event.stopPropagation();
    }
  };
  const onWheel = (event) => {
    // 画布上的滚轮/触控板手势全部由这里处理，空白处不再交给 G6 的 scroll-canvas：
    // 两套实现会按"鼠标停在哪"分工，其中一套状态卡住时表现就是"同一个位置拖几次就
    // 不动了，挪个地方又好了"。这里只有一条路径，并且和指针拖动共用按帧合并 + 兜底。
    event.preventDefault();
    event.stopPropagation();
    if (event.ctrlKey || event.metaKey) {
      const origin = container.getBoundingClientRect();
      const delta = Number(event.deltaY) || Number(event.deltaX) || 0;
      const ratio = Math.pow(1.001, -delta);
      pendingZoom = {
        ratio: (pendingZoom?.ratio || 1) * ratio,
        x: event.clientX - origin.left,
        y: event.clientY - origin.top,
      };
      scheduleGesture();
      return;
    }
    const dx = -(Number(event.deltaX) || 0);
    const dy = -(Number(event.deltaY) || 0);
    if (dx || dy) {
      pendingPan = [(pendingPan ? pendingPan[0] : 0) + dx, (pendingPan ? pendingPan[1] : 0) + dy];
      scheduleGesture();
    }
  };
  const abandonGesture = () => {
    pendingPress = null;
    pan = null;
  };
  // 窗口失焦、指针被系统取消时，容器拿不到 pointerup；不留下一个还在"拖"的状态。
  window.addEventListener("blur", abandonGesture);
  window.addEventListener("pointercancel", abandonGesture, true);
  container.addEventListener("pointerdown", onPointerDown, true);
  container.addEventListener("pointermove", onPointerMove, true);
  container.addEventListener("pointerup", onPointerUp, true);
  container.addEventListener("pointercancel", onPointerUp, true);
  container.addEventListener("lostpointercapture", onPointerUp, true);
  container.addEventListener("wheel", onWheel, { capture: true, passive: false });
}

function createSemanticGraph(container, tree, onNodeClick) {
  if (!window.G6 || typeof window.G6.Graph !== "function") {
    throw new RouteViewError("corrupt", "语义索引画布组件不可用");
  }
  const graph = new window.G6.Graph({
    container,
    autoFit: false,
    animation: false,
    zoomRange: [0.25, 2],
    padding: [40, 420, 40, 80],
    data: graphData(tree),
    node: {
      type: (data) => data.kind === "document" ? "html" : "rect",
      style: {
        size: graphNodeSize,
        dx: (data) => data.kind === "document" ?
          -(data.documentWidth || DOCUMENT_NODE_WIDTH) / 2 : 0,
        dy: (data) => data.kind === "document" ?
          -(data.documentHeight || DOCUMENT_NODE_MIN_HEIGHT) / 2 : 0,
        innerHTML: documentNodeHTML,
        radius: (data) => data.kind === "document" ? 22 : 16,
        fill: (data) => data.kind === "document" ? "#fffef3" :
          data.kind === "leaf" ? "#f5edcf" : data.kind === "more" ? "#eef3ef" : "#e4efe7",
        stroke: (data) => data.kind === "document" ? "#6f947e" :
          data.kind === "leaf" ? "#b6a56d" : "#7c9e8b",
        lineWidth: (data) => data.kind === "document" ? 2.5 : 1.5,
        labelText: (data) => data.kind === "document" ? "" : data.name,
        labelWordWrap: true,
        labelWordWrapWidth: (data) => routeNodeLayout(data.name).wrapWidth,
        labelMaxLines: (data) => routeNodeLayout(data.name).lines,
        labelTextOverflow: "…",
        labelLineHeight: ROUTE_NODE_LINE_HEIGHT,
        labelPlacement: "center",
        labelTextAlign: "center",
        labelTextBaseline: "middle",
        labelFill: "#2d4539",
        labelFontSize: 14,
        labelFontWeight: 700,
      },
    },
    edge: {
      type: "cubic-horizontal",
      style: { stroke: "#9bb1a1", lineWidth: 1.4 },
    },
    layout: {
      // G6's compact box layout cannot be used with nodes of different sizes: its
      // result carries the top-left corner of a gap-inflated box while G6 places
      // the element's centre there (`YE` ends with a uniform `translate`, never a
      // per-node half-size correction). With one node size the whole tree is
      // shifted and nobody notices; the moment a 900x527 card is laid out next
      // to a 220x72 Route box, every card is drawn half its own size up and to
      // the left of the row it was given — on top of its Route, with the edge
      // leaving at a random height. Verified in the vendored 5.1.1 by running
      // `G6.CompactBoxLayout` on a two-node tree and comparing the result with
      // the drawn bounds.
      //
      // dagre takes a size per node and answers with centres, which is what G6
      // renders, so no compensation pass is needed and none is written. Its
      // ranksep/nodesep are also per-side: the visible gap is twice the value,
      // hence 36 and 24 for the 72px the previous layout used.
      type: "antv-dagre",
      rankdir: "LR",
      ranker: "tight-tree",
      nodeSize: layoutNodeSize,
      ranksep: 36,
      nodesep: 24,
    },
    behaviors: [
      // 平移与缩放全部由 installCanvasGestureBridge 接管：画布上的空白处和卡片走同一条
      // 路径，滚轮/触控板也一样。G6 的 drag-canvas 对空白处无效，scroll-canvas /
      // zoom-canvas 又会在同一位置卡住——两套实现并存只会各自留下一种"拖不动"。
      "click-select",
      {
        type: "collapse-expand",
        key: "collapse-expand",
        trigger: "click",
        enable: (event) => {
          const node = event.targetType === "node" ?
            findTreeNode(tree, event.target.id) : null;
          return node?.kind === "branch" && node.childrenLoaded === true;
        },
        animation: false,
        align: true,
      },
    ],
  });
  container.__semanticGraph = graph;
  installCanvasGestureBridge(graph, container);
  graph.on("node:click", (event) => onNodeClick(graph, event.target.id));
  return graph;
}

async function capFocusZoom(graph) {
  const zoom = graph.getZoom?.();
  if (Number.isFinite(zoom) && zoom > CANVAS_FOCUS_MAX_ZOOM &&
      typeof graph.zoomTo === "function") {
    await graph.zoomTo(CANVAS_FOCUS_MAX_ZOOM, false);
  }
}

async function focusSemanticGraph(graph) {
  if (typeof graph.fitView === "function") {
    await graph.fitView({ animation: { duration: 180 } });
    await capFocusZoom(graph);
    return;
  }
  if (typeof graph.fitCenter === "function") {
    await graph.fitCenter({ animation: { duration: 180 } });
    await capFocusZoom(graph);
  }
}

function statusDocumentNode(node, text, state = "status") {
  return documentNode({ row_id: node.id, revision: node.revision }, text, state);
}

function statusDocumentText(title, detail) {
  return `${title}\n${detail}`;
}

function focusDocumentBranch(graph, routeID, documentID) {
  const zoom = Math.min(1, Math.max(graph.getZoom?.() || 0, 0.8));
  Promise.resolve(graph.zoomTo?.(zoom, false))
    .then(() => graph.focusElement?.([routeID, documentID], { duration: 160 }))
    .catch(() => {});
}

function focusRouteNode(graph, routeID) {
  Promise.resolve(graph.focusElement?.(routeID, { duration: 160 })).catch(() => {});
}

async function toggleLeafDocument(graph, tree, node, executeMSQL, databaseID, tableID) {
  node.documentRequest = (node.documentRequest || 0) + 1;
  const request = node.documentRequest;
  const current = findDocumentNode(node);
  if (current) {
    await replaceDocumentNode(graph, tree, node, null);
    focusRouteNode(graph, node.id);
    return;
  }

  try {
    const locators = await loadLocators(executeMSQL, databaseID, tableID, node.route_id);
    if (request !== node.documentRequest) return;
    if (locators.rows.length === 0) {
      const empty = statusDocumentNode(node, statusDocumentText("这个 Leaf 还没有 Row", "当前语义节点尚未关联活跃 Row。"), "empty");
      await replaceDocumentNode(graph, tree, node, empty);
      focusDocumentBranch(graph, node.id, empty.id);
      return;
    }
    const locator = locators.rows[0];
    const preview = await loadRowPreview(executeMSQL, locator);
    if (request !== node.documentRequest) return;
    const content = documentNode(locator, preview);
    await replaceDocumentNode(graph, tree, node, content);
    focusDocumentBranch(graph, node.id, content.id);
  } catch (error) {
    if (request !== node.documentRequest) return;
    const [kind, title, detail] = errorState(error);
    const failure = statusDocumentNode(node, statusDocumentText(title, detail), kind);
    await replaceDocumentNode(graph, tree, node, failure);
    focusDocumentBranch(graph, node.id, failure.id);
  }
}

function landingView() {
  const view = element("div", "catalog-view route-view");
  view.append(breadcrumbs([]), heading("Semantic Index", "每棵语义索引属于一个 Table。"));
  const guide = stateNode("empty", "请先选择一个 Table", "从 Catalog 的 Table Schema 页面进入对应 Route Tree。");
  const link = element("a", "route-entry", "打开 Catalog");
  link.href = "/catalog";
  link.dataset.route = "";
  guide.append(link);
  view.append(guide);
  return view;
}

export async function renderRoutes(root, options) {
  const previousCanvas = root.querySelector(".semantic-canvas");
  previousCanvas?.__semanticGraph?.destroy?.();
  showState(root, "loading", "正在读取语义索引", "加载根节点；展开分支时按需读取下一层…");
  try {
    const parts = pathParts(options.path);
    if (parts.length === 0) {
      root.dataset.pageState = "ready";
      root.replaceChildren(landingView());
      return;
    }
    const databaseID = stableID(parts[0], "db_", "Database");
    const tableID = stableID(parts[1], "tbl_", "Table");
    const data = await loadTableRoot(options.executeMSQL, databaseID, tableID);
    if (!options.isCurrent()) return;
    const tree = treeRoot(data.object, data.rows);
    const view = element("div", "semantic-canvas-page semantic-canvas-fullscreen");
    const controls = element("div", "semantic-canvas-controls");
    const back = element("a", "canvas-control canvas-back", "返回库");
    back.href = `/catalog/${encodeURIComponent(databaseID)}`;
    back.dataset.route = "";
    back.setAttribute("aria-label", `返回 ${data.database.name} 库`);
    back.title = `返回 ${data.database.name} 库`;
    controls.append(back);
    const focusButton = element("button", "canvas-focus-button", "聚焦到中心");
    focusButton.type = "button";
    focusButton.setAttribute("aria-label", "聚焦到语义索引中心");
    focusButton.title = "将当前已加载的语义索引重新适配到画布中心";
    controls.append(focusButton);
    const stage = element("div", "semantic-canvas-stage");
    const canvas = element("div", "semantic-canvas");
    canvas.setAttribute("aria-label", "语义索引树无限画布");
    stage.append(controls, canvas);
    view.append(stage);
    root.dataset.pageState = data.rows.length === 0 ? "empty" : "ready";
    root.replaceChildren(view);
    if (data.rows.length === 0) {
      setCanvasState(stage, "empty", "这个 Table 还没有 Route", "语义索引建立后，第一层节点会显示在这里。");
      return;
    }
    const graph = createSemanticGraph(canvas, tree, async (currentGraph, nodeID) => {
      const node = findTreeNode(tree, nodeID);
      if (!node) return;
      clearCanvasState(stage);
      currentGraph.setElementState(nodeID, "selected");
      if (node.kind === "branch") {
        if (!node.childrenLoaded) {
          try {
            await appendChildren(graph, tree, node, options.executeMSQL, databaseID, tableID);
          } catch (error) {
            const [kind, title, detail] = errorState(error);
            setCanvasState(stage, kind, title, detail);
          }
        }
        return;
      }
      if (node.kind !== "leaf") return;
      await toggleLeafDocument(currentGraph, tree, node, options.executeMSQL, databaseID, tableID);
    });
    focusButton.addEventListener("click", () => { void focusSemanticGraph(graph); });
    await graph.render();
    await focusSemanticGraph(graph);
    if (parts.length === 3) {
      const routeID = stableID(parts[2], "route_", "Route");
      let selected = findTreeNode(tree, routeID);
      if (!selected) {
        selected = await expandToRoute(graph, tree, routeID, options.executeMSQL, databaseID, tableID);
        if (!options.isCurrent()) return;
      }
      if (selected) {
        clearCanvasState(stage);
        graph.setElementState(routeID, "selected");
        await graph.focusElement?.(routeID, { duration: 250 });
      } else {
        setCanvasState(stage, "empty", "找不到这个节点",
          "深链路指向的 Route 不在这个 Table 里，或已经被移除。");
      }
    }
  } catch (error) {
    if (!options.isCurrent()) return;
    const [kind, title, detail] = errorState(error);
    showState(root, kind, title, detail);
  }
}
