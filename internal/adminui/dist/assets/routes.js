const CHILD_LIMIT = 12;
const LOCATOR_LIMIT = 1;

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
  const childKeys = ["route_id", "parent_id", "path", "name", "aliases", "kind", "purpose", "revision"];
  const pointKeys = [...childKeys, "database_id", "table_id", "synopsis"];
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
    return ["error", "Route Tree 已发生变化", "当前分页快照已失效，请刷新这一层后继续。"];
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

function heading(title, purpose, meta) {
  const wrapper = element("header", "catalog-heading route-heading");
  const content = element("div");
  content.append(element("h2", "", title), element("p", "", purpose));
  const badges = element("div", "catalog-meta");
  for (const value of meta) badges.append(element("span", "schema-badge", value));
  content.append(badges);
  wrapper.append(content);
  return wrapper;
}

function routeCard(row, databaseID, tableID) {
  const card = element("a", "route-node");
  card.href = `/routes/${encodeURIComponent(databaseID)}/${encodeURIComponent(tableID)}/${encodeURIComponent(row.route_id)}`;
  card.dataset.route = "";
  const marker = element("span", `route-kind route-kind-${row.kind}`, row.kind);
  const text = element("div");
  text.append(element("strong", "", row.name), element("p", "", row.purpose),
    element("small", "", `${row.path} · revision ${row.revision}`));
  card.append(marker, text);
  return card;
}

function locatorCard(row) {
  const card = element("a", "locator-card");
  card.href = `/rows/${encodeURIComponent(row.database_id)}/${encodeURIComponent(row.table_id)}/${encodeURIComponent(row.row_id)}`;
  card.dataset.route = "";
  card.append(element("strong", "", row.row_id), element("span", "", `revision ${row.revision}`));
  const scope = element("small", "", `${row.database_id} / ${row.table_id}`);
  card.append(scope);
  return card;
}

function markComplete(section) {
  const root = section.closest(".route-outlet");
  if (root) root.dataset.pageState = "ready";
}

function pagedSection(title, rows, page, render, loadMore) {
  const section = element("section", "catalog-section route-section");
  const header = element("div", "section-heading");
  header.append(element("h3", "", title), element("span", "", `snapshot ${page.snapshot.slice(0, 18)}…`));
  const list = element("div", "route-list");
  for (const row of rows) list.append(render(row));
  section.append(header, list);
  if (page.truncated) addContinuation(section, list, rows, page, render, loadMore);
  return section;
}

function addContinuation(section, list, rows, page, render, loadMore) {
  const button = element("button", "load-more", "加载下一页");
  button.type = "button";
  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      const next = await loadMore(page.next_cursor);
      if (next.page.snapshot !== page.snapshot) {
        throw new RouteViewError("revision_conflict", "Route page snapshot changed");
      }
      const known = new Set(rows.map((row) => row.route_id || row.row_id));
      for (const row of next.rows) {
        const id = row.route_id || row.row_id;
        if (known.has(id)) throw new RouteViewError("corrupt", "Route continuation duplicated an object");
        known.add(id);
        rows.push(row);
        list.append(render(row));
      }
      button.remove();
      if (next.page.truncated) addContinuation(section, list, rows, next.page, render, loadMore);
      else markComplete(section);
    } catch (error) {
      const [kind, stateTitle, detail] = errorState(error);
      section.append(stateNode(kind, stateTitle, detail));
      button.disabled = false;
    }
  });
  section.append(button);
}

async function loadTableRoot(executeMSQL, databaseID, tableID, cursor = "") {
  const subject = `${quoteIdentifier(databaseID, "db_")}.${quoteIdentifier(tableID, "tbl_")}`;
  if (cursor) {
    const source = `SHOW ROUTES FROM TABLE ${subject} AT ROOT CURSOR :cursor LIMIT 12`;
    const result = resultsFrom(await executeMSQL(source, [statementInput({ cursor })]), 1)[0];
    return { rows: routeRows(result, databaseID, tableID), page: validatePage(result, CHILD_LIMIT) };
  }
  const source = `DESCRIBE TABLE ${subject} COMPACT; SHOW ROUTES FROM TABLE ${subject} AT ROOT LIMIT 12`;
  const results = resultsFrom(await executeMSQL(source), 2);
  return {
    object: tablePoint(results[0], databaseID, tableID),
    rows: routeRows(results[1], databaseID, tableID),
    page: validatePage(results[1], CHILD_LIMIT)
  };
}

async function describeRoute(executeMSQL, databaseID, tableID, routeID) {
  stableID(routeID, "route_", "Route");
  const result = resultsFrom(await executeMSQL("DESCRIBE ROUTE :route", [statementInput({ route: routeID })]), 1)[0];
  if (result.rows.length !== 1) throw new RouteViewError("corrupt", "Route point result is invalid");
  return validateRouteRow(result.rows[0], true, databaseID, tableID);
}

async function loadChildren(executeMSQL, databaseID, tableID, routeID, cursor = "") {
  const source = cursor ? "SHOW ROUTES UNDER :route CURSOR :cursor LIMIT 12" :
    "SHOW ROUTES UNDER :route LIMIT 12";
  const named = cursor ? { route: routeID, cursor } : { route: routeID };
  const result = resultsFrom(await executeMSQL(source, [statementInput(named)]), 1)[0];
  return {
    rows: routeRows(result, databaseID, tableID, routeID),
    page: validatePage(result, CHILD_LIMIT)
  };
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

const DOCUMENT_NODE_WIDTH = 900;
const DOCUMENT_NODE_MIN_HEIGHT = 620;
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
  const header = element("header", "semantic-document-header");
  header.append(element("p", "semantic-document-kicker", "MEMORA ROW"));

  if (state !== "ready") {
    const lines = String(preview).split("\n");
    header.append(element("h2", "", lines.shift() || "Row 内容"));
    const body = element("div", "semantic-document-status");
    body.append(element("p", "", lines.join("\n")));
    article.append(header, body);
    return article;
  }

  if (!preview.row) {
    header.append(element("h2", "", "当前 Row 不可见"));
    const body = element("div", "semantic-document-status");
    body.append(element("p", "", "locator 仍存在，但当前 revision 没有 live value。"));
    article.append(header, body);
    return article;
  }

  header.append(element("h2", "", documentTitle(locator, preview)),
    element("p", "semantic-document-semantics", preview.detail.row_semantics || "当前语义记录"));

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

  const properties = element("footer", "semantic-document-properties");
  properties.append(element("h3", "", "记录字段"));
  const list = element("dl", "semantic-document-property-list");
  for (const column of preview.columns) {
    if (SYSTEM_COLUMNS.includes(column.name) || column.name === titleColumn ||
        column.name === summaryColumn) continue;
    const item = element("div", "semantic-document-property");
    const term = element("dt", "", column.name);
    term.append(element("span", "", column.type));
    item.append(term, element("dd", "", displayValue(preview.row[column.name])));
    list.append(item);
  }
  if (list.childElementCount) properties.append(list);
  else properties.append(element("p", "semantic-document-empty", "没有其他业务字段。"));
  article.append(properties);
  return article;
}

function measuredDocumentHeight(article) {
  const probe = article.cloneNode(true);
  probe.classList.add("semantic-document-measure");
  document.body.append(probe);
  const height = Math.ceil(probe.getBoundingClientRect().height);
  probe.remove();
  return Math.max(DOCUMENT_NODE_MIN_HEIGHT, height);
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

function graphNodeSize(data) {
  if (data?.kind === "document") {
    return [data.documentWidth || DOCUMENT_NODE_WIDTH, data.documentHeight || DOCUMENT_NODE_MIN_HEIGHT];
  }
  return [220, 72];
}

function layoutNodeData(node) {
  return node?.data?.data || node?.data || node || {};
}

function isDocumentLayoutNode(node) {
  const data = layoutNodeData(node);
  return data.kind === "document" || String(data.id || node?.id || "").startsWith("row_document_");
}

function graphNodeWidth(node) {
  const data = layoutNodeData(node);
  return isDocumentLayoutNode(node) ? data.documentWidth || DOCUMENT_NODE_WIDTH : 220;
}

function graphNodeHeight(node) {
  const data = layoutNodeData(node);
  return isDocumentLayoutNode(node) ? data.documentHeight || DOCUMENT_NODE_MIN_HEIGHT : 72;
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
    page: null
  };
}

function treeRoot(table, rows, page) {
  const root = {
    id: "table-root",
    name: table.name,
    kind: "root",
    purpose: table.row_semantics,
    children: rows.map(treeNode),
    childrenLoaded: true,
    page
  };
  if (page.truncated) root.children.push(moreNode(root, page.next_cursor));
  return root;
}

function moreNode(parent, cursor) {
  const suffix = String(cursor).replace(/[^A-Za-z0-9_-]/g, "").slice(-24) || "next";
  return {
    id: `route_more_${parent.id}_${suffix}`,
    name: "继续加载这一层",
    kind: "more",
    purpose: "这一层还有更多语义节点",
    parent_id: parent.id,
    moreParent: parent.id,
    moreCursor: cursor,
    children: []
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
  return window.G6.treeToGraphData(tree);
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

async function placeDocumentAfterLeaf(graph, leaf, document) {
  if (!document || typeof graph.getElementPosition !== "function" ||
      typeof graph.translateElementTo !== "function") return;
  const position = graph.getElementPosition(leaf.id);
  const x = Array.isArray(position) ? position[0] : position?.x;
  const y = Array.isArray(position) ? position[1] : position?.y;
  if (!Number.isFinite(x) || !Number.isFinite(y)) return;
  const documentWidth = document.documentWidth || DOCUMENT_NODE_WIDTH;
  await graph.translateElementTo(document.id, [x + 110 + 72 + documentWidth / 2, y], false);
}

async function replaceDocumentNode(graph, tree, node, document) {
  node.children = document ? [document] : [];
  graph.setData(graphData(tree));
  await graph.render();
  await placeDocumentAfterLeaf(graph, node, document);
  return document;
}

async function appendChildren(graph, tree, node, executeMSQL, databaseID, tableID, cursor = "") {
  const next = await loadChildren(executeMSQL, databaseID, tableID, node.route_id, cursor);
  const existing = cursor ? node.children.filter((child) => child.kind !== "more") : [];
  node.children = existing.concat(next.rows.map(treeNode));
  if (next.page.truncated) node.children.push(moreNode(node, next.page.next_cursor));
  node.childrenLoaded = true;
  node.page = next.page;
  const selected = node.id;
  graph.setData(graphData(tree));
  await graph.render();
  if (graph.expandElement) await graph.expandElement(selected, { animation: false });
}

async function appendRootPage(graph, tree, executeMSQL, databaseID, tableID, cursor) {
  const next = await loadTableRoot(executeMSQL, databaseID, tableID, cursor);
  const existing = tree.children.filter((child) => child.kind !== "more");
  tree.children = existing.concat(next.rows.map(treeNode));
  if (next.page.truncated) tree.children.push(moreNode(tree, next.page.next_cursor));
  tree.page = next.page;
  graph.setData(graphData(tree));
  await graph.render();
}

function createSemanticGraph(container, tree, onNodeClick) {
  if (!window.G6 || typeof window.G6.Graph !== "function") {
    throw new RouteViewError("corrupt", "语义索引画布组件不可用");
  }
  const graph = new window.G6.Graph({
    container,
    autoFit: "view",
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
      type: "compact-box",
      direction: "LR",
      getWidth: graphNodeWidth,
      getHeight: graphNodeHeight,
      getHGap: () => 72,
      getVGap: () => 22,
    },
    behaviors: [
      "drag-canvas", "zoom-canvas", "click-select",
      {
        type: "collapse-expand",
        key: "collapse-expand",
        trigger: "click",
        enable: (event) => event.targetType === "node" &&
          findTreeNode(tree, event.target.id)?.kind === "branch",
        animation: true,
        align: true,
      },
    ],
  });
  graph.on("node:click", (event) => onNodeClick(graph, event.target.id));
  return graph;
}

function focusSemanticGraph(graph) {
  if (typeof graph.fitView === "function") {
    graph.fitView({ animation: { duration: 300 } });
    return;
  }
  if (typeof graph.fitCenter === "function") graph.fitCenter({ animation: { duration: 300 } });
}

function statusDocumentNode(node, text, state = "status") {
  return documentNode({ row_id: node.id, revision: node.revision }, text, state);
}

function statusDocumentText(title, detail) {
  return `${title}\n${detail}`;
}

function focusDocumentBranch(graph, routeID, documentID) {
  const zoom = Math.min(1, Math.max(graph.getZoom?.() || 0, 0.8));
  Promise.resolve(graph.zoomTo?.(zoom, { duration: 220 }))
    .then(() => graph.focusElement?.([routeID, documentID], { duration: 260 }))
    .catch(() => {});
}

async function toggleLeafDocument(graph, tree, node, executeMSQL, databaseID, tableID) {
  node.documentRequest = (node.documentRequest || 0) + 1;
  const request = node.documentRequest;
  const current = findDocumentNode(node);
  if (current) {
    await replaceDocumentNode(graph, tree, node, null);
    focusSemanticGraph(graph);
    return;
  }

  const pending = statusDocumentNode(node, statusDocumentText("正在读取 Row 内容", "先打开唯一 locator，再按 RowID 回表…"));
  await replaceDocumentNode(graph, tree, node, pending);
  focusDocumentBranch(graph, node.id, pending.id);
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
  view.append(breadcrumbs([]), heading("Route Tree", "每棵语义索引属于一个 Table。", ["read only"]));
  const guide = stateNode("empty", "请先选择一个 Table", "从 Catalog 的 Table Schema 页面进入对应 Route Tree。");
  const link = element("a", "route-entry", "打开 Catalog");
  link.href = "/catalog";
  link.dataset.route = "";
  guide.append(link);
  view.append(guide);
  return view;
}

export async function renderRoutes(root, options) {
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
    const tree = treeRoot(data.object, data.rows, data.page);
    const view = element("div", "semantic-canvas-page");
    view.append(breadcrumbs([{ label: data.object.name, href: `/catalog/${encodeURIComponent(databaseID)}/${encodeURIComponent(tableID)}` }]),
      heading(`${data.object.name} 语义索引`, "在这张 Table 的语义路由树中定位 Row。点击 Branch 展开，点击 Leaf 在画布中展开 Row 内容。",
        [data.object.table_id, `schema v${data.object.schema_version}`]));
    const toolbar = element("div", "semantic-canvas-toolbar");
    toolbar.append(element("span", "canvas-hint", "拖动画布 · 滚轮缩放 · 点击 Branch 展开/收起 · 点击 Leaf 展开内容"));
    const focusButton = element("button", "canvas-focus-button", "聚焦到中心");
    focusButton.type = "button";
    focusButton.setAttribute("aria-label", "聚焦到语义索引中心");
    focusButton.title = "将当前已加载的语义索引重新适配到画布中心";
    toolbar.append(focusButton);
    const stage = element("div", "semantic-canvas-stage");
    const canvas = element("div", "semantic-canvas");
    canvas.setAttribute("aria-label", "语义索引树无限画布");
    stage.append(canvas);
    view.append(toolbar, stage);
    root.dataset.pageState = data.rows.length === 0 ? "empty" : data.page.truncated ? "truncated" : "ready";
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
      if (node.kind === "more") {
        const parent = findTreeNode(tree, node.moreParent);
        if (!parent) return;
        try {
          await (parent.id === tree.id ? appendRootPage(graph, tree, options.executeMSQL, databaseID, tableID, node.moreCursor) :
            appendChildren(graph, tree, parent, options.executeMSQL, databaseID, tableID, node.moreCursor));
        } catch (error) {
          const [kind, title, detail] = errorState(error);
          setCanvasState(stage, kind, title, detail);
        }
        return;
      }
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
    focusButton.addEventListener("click", () => focusSemanticGraph(graph));
    await graph.render();
    if (parts.length === 3) {
      const routeID = stableID(parts[2], "route_", "Route");
      const selected = findTreeNode(tree, routeID);
      if (selected) {
        await graph.focusElement?.(routeID, { duration: 250 });
      } else {
        setCanvasState(stage, "empty", "节点尚未展开", "这个深链路节点位于未加载的语义分支，请从根节点逐层展开。");
      }
    }
  } catch (error) {
    if (!options.isCurrent()) return;
    const [kind, title, detail] = errorState(error);
    showState(root, kind, title, detail);
  }
}
