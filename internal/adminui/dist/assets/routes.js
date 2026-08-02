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
  const childKeys = ["route_id", "parent_id", "path", "name", "kind", "purpose", "revision"];
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
  showState(root, "loading", "正在读取 Route Tree", "只加载当前 node 和一层相邻对象…");
  try {
    const parts = pathParts(options.path);
    if (parts.length === 0) {
      root.dataset.pageState = "ready";
      root.replaceChildren(landingView());
      return;
    }
    const databaseID = stableID(parts[0], "db_", "Database");
    const tableID = stableID(parts[1], "tbl_", "Table");
    const tableURL = `/routes/${encodeURIComponent(databaseID)}/${encodeURIComponent(tableID)}`;
    const view = element("div", "catalog-view route-view");
    if (parts.length === 2) {
      const data = await loadTableRoot(options.executeMSQL, databaseID, tableID);
      if (!options.isCurrent()) return;
      view.append(breadcrumbs([{ label: data.object.name }]),
        heading(`${data.object.name} Route Tree`, data.object.row_semantics,
          [data.object.table_id, `schema v${data.object.schema_version}`]));
      if (data.rows.length === 0) {
        view.append(stateNode("empty", "这个 Table 还没有 Route", "语义索引建立后，第一层节点会显示在这里。"));
      } else {
        const parentID = data.rows[0].parent_id;
        view.append(pagedSection("Root routes", data.rows, data.page,
          (row) => routeCard(row, databaseID, tableID),
          async (cursor) => {
            const next = await loadTableRoot(options.executeMSQL, databaseID, tableID, cursor);
            if (next.rows.some((row) => row.parent_id !== parentID)) {
              throw new RouteViewError("corrupt", "Route root continuation changed parent");
            }
            return next;
          }));
      }
      root.dataset.pageState = data.rows.length === 0 ? "empty" : data.page.truncated ? "truncated" : "ready";
      root.replaceChildren(view);
      return;
    }
    const routeID = stableID(parts[2], "route_", "Route");
    const node = await describeRoute(options.executeMSQL, databaseID, tableID, routeID);
    if (!options.isCurrent()) return;
    const data = node.kind === "leaf" ?
      await loadLocators(options.executeMSQL, databaseID, tableID, routeID) :
      await loadChildren(options.executeMSQL, databaseID, tableID, routeID);
    if (!options.isCurrent()) return;
    view.append(breadcrumbs([{ label: tableID, href: tableURL }, { label: node.name }]),
      heading(node.name, node.purpose, [node.kind, node.route_id, `revision ${node.revision}`]));
    if (node.synopsis) view.append(element("p", "route-synopsis", node.synopsis));
    if (data.rows.length === 0) {
      const title = node.kind === "leaf" ? "这个 leaf 还没有 locator" : "这个节点还没有 child Route";
      view.append(stateNode("empty", title, "当前语义索引层没有可见对象。"));
    } else if (node.kind === "leaf") {
      view.append(pagedSection("Row locators", data.rows, data.page, locatorCard,
        () => loadLocators(options.executeMSQL, databaseID, tableID, routeID)));
    } else {
      view.append(pagedSection("Child routes", data.rows, data.page,
        (row) => routeCard(row, databaseID, tableID),
        (cursor) => loadChildren(options.executeMSQL, databaseID, tableID, routeID, cursor)));
    }
    root.dataset.pageState = data.rows.length === 0 ? "empty" : data.page.truncated ? "truncated" : "ready";
    root.replaceChildren(view);
  } catch (error) {
    if (!options.isCurrent()) return;
    const [kind, title, detail] = errorState(error);
    showState(root, kind, title, detail);
  }
}
