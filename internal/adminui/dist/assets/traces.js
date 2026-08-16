const DATABASE_LIMIT = 32;
const TRACE_LIMIT = 20;
const STEP_LIMIT = 24;
const TRACE_COLUMNS = [
  ["version", "TEXT", false], ["trace_id", "ID", false],
  ["trace_sequence", "INTEGER", false], ["recorded_at", "TIMESTAMP", false],
  ["expires_at", "TIMESTAMP", false], ["actor", "TEXT", false],
  ["database_id", "ID", false], ["table_id", "ID", false],
  ["status", "TEXT", false], ["step_count", "INTEGER", false], ["checksum", "TEXT", false]
];
const STEP_COLUMNS = [
  ["trace_id", "ID", false], ["trace_sequence", "INTEGER", false],
  ["database_id", "ID", false], ["table_id", "ID", false],
  ["ordinal", "INTEGER", false], ["operation", "TEXT", false],
  ["parent_route_id", "ID", true], ["candidate_route_ids", "ID_LIST", false],
  ["selected_route_id", "ID", true], ["locators", "ROW_LOCATOR_LIST", false],
  ["outcome", "TEXT", false], ["elapsed_ms", "INTEGER", false],
  ["remaining_budget", "INTEGER", false]
];
const STATUSES = new Set(["complete", "partial", "no_results", "access_denied", "failed"]);
const OPERATIONS = new Set(["FRAME", "CHOOSE", "OPEN", "SELECT", "FALLBACK"]);
const OUTCOMES = new Set([
  "succeeded", "not_found", "permission_denied", "revision_conflict",
  "output_truncated", "cancelled", "failed"
]);

class TraceViewError extends Error {
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
    throw new TraceViewError("corrupt", `${label} stable ID is invalid`);
  }
  return value;
}

function traceID(value) {
  if (typeof value !== "string" || !/^trace_[a-f0-9]{32}$/.test(value)) {
    throw new TraceViewError("corrupt", "Trace ID is invalid");
  }
  return value;
}

function safeText(value, label, limit = 4096) {
  if (typeof value !== "string" || value.length === 0 || value.length > limit) {
    throw new TraceViewError("corrupt", `${label} text is invalid`);
  }
  return value;
}

function quoteDatabase(value) {
  stableID(value, "db_", "Database");
  return `"${value.replaceAll('"', '""')}"`;
}

function statementInput(named) {
  return { parameters: { named } };
}

function pathParts(path) {
  const raw = path.split("/").filter(Boolean);
  if (raw[0] !== "traces" || raw.length > 4 || raw.length === 3) {
    throw new TraceViewError("corrupt", "Route Trace path is invalid");
  }
  try {
    return raw.slice(1).map(decodeURIComponent);
  } catch (_) {
    throw new TraceViewError("corrupt", "Route Trace path encoding is invalid");
  }
}

function firstFailure(envelope) {
  if (envelope && envelope.error) return envelope.error;
  if (envelope && Array.isArray(envelope.results)) {
    const failed = envelope.results.find((result) => result && result.error);
    if (failed) return failed.error;
  }
  return { code: "corrupt" };
}

function resultsFrom(envelope, count) {
  if (!envelope || envelope.version !== "memora.result/v1" || !Array.isArray(envelope.results)) {
    throw new TraceViewError("corrupt", "Route Trace result envelope is invalid");
  }
  if (!envelope.ok) {
    const failure = firstFailure(envelope);
    throw new TraceViewError(failure.code || "corrupt", "Route Trace request failed");
  }
  if (envelope.results.length !== count || envelope.results.some((result) =>
    !result || result.status !== "succeeded" || !Array.isArray(result.rows) ||
    !Array.isArray(result.columns))) {
    throw new TraceViewError("corrupt", "Route Trace result shape is invalid");
  }
  return envelope.results;
}

function exactKeys(value, expected) {
  return value && !Array.isArray(value) &&
    Object.keys(value).sort().join("\u0000") === [...expected].sort().join("\u0000");
}

function positiveInteger(value) {
  return Number.isSafeInteger(value) && value > 0;
}

function nonnegativeInteger(value) {
  return Number.isSafeInteger(value) && value >= 0;
}

function validTimestamp(value) {
  return typeof value === "string" && value.length <= 64 && Number.isFinite(Date.parse(value));
}

function validateColumns(columns, expected) {
  if (columns.length !== expected.length) {
    throw new TraceViewError("corrupt", "Route Trace columns are incomplete");
  }
  for (let index = 0; index < expected.length; index += 1) {
    const column = columns[index];
    const [name, type, nullable] = expected[index];
    if (!exactKeys(column, ["name", "type", "nullable"]) || column.name !== name ||
        column.type !== type || column.nullable !== nullable) {
      throw new TraceViewError("corrupt", "Route Trace columns do not match the frozen contract");
    }
  }
}

function validatePage(result, limit) {
  const page = result.page;
  if (!page || page.version !== "memora.list-page/v1" || page.limit !== limit ||
      typeof page.cursor !== "string" || !/^sha256:[a-f0-9]{64}$/.test(page.snapshot) ||
      typeof page.truncated !== "boolean" ||
      (page.truncated && (typeof page.next_cursor !== "string" || page.next_cursor.length === 0)) ||
      (!page.truncated && page.next_cursor !== undefined && page.next_cursor !== "")) {
    throw new TraceViewError("corrupt", "Route Trace page contract is invalid");
  }
  return page;
}

function validateDatabaseRows(rows) {
  const seen = new Set();
  for (const row of rows) {
    const fields = [
      "database_id", "name", "aliases", "purpose", "scope", "schema_version",
      "tables", "created_at", "updated_at"
    ];
    if (row.anti_scope !== undefined) fields.push("anti_scope");
    if (!exactKeys(row, fields)) throw new TraceViewError("corrupt", "Database summary is invalid");
    stableID(row.database_id, "db_", "Database");
    safeText(row.name, "Database name", 256);
    safeText(row.purpose, "Database purpose");
    safeText(row.scope, "Database scope");
    if (row.anti_scope !== undefined) safeText(row.anti_scope, "Database anti-scope");
    if (!positiveInteger(row.schema_version) || !Array.isArray(row.aliases) ||
        !Array.isArray(row.tables) || row.tables.length !== 0 ||
        !validTimestamp(row.created_at) || !validTimestamp(row.updated_at) ||
        seen.has(row.database_id)) {
      throw new TraceViewError("corrupt", "Database summary identity is invalid");
    }
    const aliases = new Set();
    for (const alias of row.aliases) {
      safeText(alias, "Database alias", 256);
      if (aliases.has(alias)) throw new TraceViewError("corrupt", "Database aliases are duplicated");
      aliases.add(alias);
    }
    seen.add(row.database_id);
  }
  return rows;
}

function pointDatabase(result, databaseID) {
  const rows = validateDatabaseRows(result.rows);
  if (rows.length !== 1 || rows[0].database_id !== databaseID) {
    throw new TraceViewError("corrupt", "Database point response is invalid");
  }
  return rows[0];
}

function validateTimeline(result, databaseID) {
  validateColumns(result.columns, TRACE_COLUMNS);
  const fields = TRACE_COLUMNS.map((column) => column[0]);
  const seen = new Set();
  let previous = 0;
  for (const row of result.rows) {
    if (!exactKeys(row, fields) || row.version !== "memora.route-trace/v1" ||
        traceID(row.trace_id) !== row.trace_id || !positiveInteger(row.trace_sequence) ||
        row.trace_sequence <= previous || !validTimestamp(row.recorded_at) ||
        !validTimestamp(row.expires_at) || Date.parse(row.expires_at) <= Date.parse(row.recorded_at) ||
        !safeText(row.actor, "Trace actor", 160) || row.database_id !== databaseID ||
        stableID(row.table_id, "tbl_", "Table") !== row.table_id || !STATUSES.has(row.status) ||
        !positiveInteger(row.step_count) || row.step_count > 64 ||
        typeof row.checksum !== "string" || !/^[a-f0-9]{64}$/.test(row.checksum) ||
        seen.has(row.trace_id)) {
      throw new TraceViewError("corrupt", "Route Trace summary is invalid");
    }
    previous = row.trace_sequence;
    seen.add(row.trace_id);
  }
  return { rows: result.rows, page: validatePage(result, TRACE_LIMIT) };
}

function validateRouteIDs(ids) {
  if (!Array.isArray(ids) || ids.length > 24) {
    throw new TraceViewError("corrupt", "Trace candidates are invalid");
  }
  const seen = new Set();
  for (const id of ids) {
    stableID(id, "route_", "Route");
    if (seen.has(id)) throw new TraceViewError("corrupt", "Trace candidates are duplicated");
    seen.add(id);
  }
  return seen;
}

function validateLocators(locators) {
  if (!Array.isArray(locators) || locators.length > 24) {
    throw new TraceViewError("corrupt", "Trace locators are invalid");
  }
  const seen = new Set();
  for (const locator of locators) {
    if (!exactKeys(locator, ["row_id", "revision"]) ||
        stableID(locator.row_id, "row_", "Row") !== locator.row_id ||
        !positiveInteger(locator.revision) || seen.has(locator.row_id)) {
      throw new TraceViewError("corrupt", "Trace locator is invalid");
    }
    seen.add(locator.row_id);
  }
}

function validateSteps(result, databaseID, tableID, expectedTraceID) {
  validateColumns(result.columns, STEP_COLUMNS);
  const fields = STEP_COLUMNS.map((column) => column[0]);
  let previous = 0;
  let sequence = 0;
  for (const row of result.rows) {
    if (!exactKeys(row, fields) || row.trace_id !== expectedTraceID ||
        !positiveInteger(row.trace_sequence) || (sequence && row.trace_sequence !== sequence) ||
        row.database_id !== databaseID || row.table_id !== tableID ||
        !positiveInteger(row.ordinal) || (previous && row.ordinal !== previous + 1) ||
        !OPERATIONS.has(row.operation) || typeof row.parent_route_id !== "string" ||
        typeof row.selected_route_id !== "string" || !OUTCOMES.has(row.outcome) ||
        !nonnegativeInteger(row.elapsed_ms) || row.elapsed_ms > 86_400_000 ||
        !nonnegativeInteger(row.remaining_budget) || row.remaining_budget > 10_000) {
      throw new TraceViewError("corrupt", "Route Trace step is invalid");
    }
    if (row.parent_route_id) stableID(row.parent_route_id, "route_", "Parent Route");
    const candidates = validateRouteIDs(row.candidate_route_ids);
    if (row.selected_route_id) {
      stableID(row.selected_route_id, "route_", "Selected Route");
      if (candidates.size > 0 && !candidates.has(row.selected_route_id)) {
        throw new TraceViewError("corrupt", "Selected Route is outside the candidate frame");
      }
    }
    validateLocators(row.locators);
    sequence = row.trace_sequence;
    previous = row.ordinal;
  }
  const page = validatePage(result, STEP_LIMIT);
  if (page.cursor === "" && result.rows.length && result.rows[0].ordinal !== 1) {
    throw new TraceViewError("corrupt", "Route Trace does not start at ordinal 1");
  }
  return { rows: result.rows, page, sequence };
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
  if (error.code === "not_found") {
    return ["empty", "找不到这次 Route Trace", "收据不存在，或已被 retention 清理。"];
  }
  if (error.code === "permission_denied") {
    return ["permission", "无权查看 Route Trace", "当前 Admin session 未授权该 Database。"];
  }
  if (error.code === "revision_conflict" || error.code === "validation_error") {
    return ["revision_conflict", "Trace 快照无法继续", "retention epoch 或 cursor 已变化，请刷新页面。"];
  }
  if (error.code === "corrupt" || error.code === "internal_error") {
    return ["corrupt", "Route Trace 响应无法验证", "页面拒绝展示错 scope、乱序或不完整的观察收据。"];
  }
  return ["error", "暂时无法读取 Route Trace", "请确认 daemon 正常运行后重试。"];
}

function breadcrumbs(database, trace) {
  const node = element("nav", "breadcrumbs");
  node.setAttribute("aria-label", "Route Trace 路径");
  const root = element("a", "", "Route Traces");
  root.href = "/traces";
  root.dataset.route = "";
  node.append(root);
  if (database) {
    const link = trace ? element("a", "", database.name) : element("span", "", database.name);
    if (trace) {
      link.href = `/traces/${encodeURIComponent(database.database_id)}`;
      link.dataset.route = "";
    }
    node.append(element("span", "", "/"), link);
  }
  if (trace) node.append(element("span", "", "/"), element("span", "", trace));
  return node;
}

function heading(title, purpose, database, extra) {
  const wrapper = element("header", "catalog-heading trace-heading");
  const text = element("div");
  text.append(element("h2", "", title));
  if (purpose) text.append(element("p", "", purpose));
  wrapper.append(text);
  return wrapper;
}

function databaseCard(row) {
  const card = element("a", "object-card");
  card.href = `/traces/${encodeURIComponent(row.database_id)}`;
  card.dataset.route = "";
  card.append(element("strong", "", row.name), element("p", "", row.purpose));
  return card;
}

function traceCard(row) {
  const card = element("a", "trace-card");
  card.href = `/traces/${encodeURIComponent(row.database_id)}/${encodeURIComponent(row.table_id)}/` +
    `${encodeURIComponent(row.trace_id)}`;
  card.dataset.route = "";
  const header = element("div", "trace-card-heading");
  const identity = element("div");
  identity.append(element("strong", "", `Trace ${row.trace_sequence}`));
  const status = element("span", "trace-status", row.status);
  status.dataset.status = row.status;
  header.append(identity, status);
  card.append(header, element("p", "", `${row.step_count} steps · ${row.table_id}`),
    element("small", "", `${row.actor} · ${row.recorded_at} → ${row.expires_at}`));
  return card;
}

function routeLink(databaseID, tableID, routeID, className = "trace-id-link") {
  const link = element("a", className, routeID);
  link.href = `/routes/${encodeURIComponent(databaseID)}/${encodeURIComponent(tableID)}/` +
    `${encodeURIComponent(routeID)}`;
  link.dataset.route = "";
  return link;
}

function locatorLink(databaseID, tableID, locator) {
  const link = element("a", "trace-locator");
  link.href = `/rows/${encodeURIComponent(databaseID)}/${encodeURIComponent(tableID)}/` +
    `${encodeURIComponent(locator.row_id)}`;
  link.dataset.route = "";
  link.setAttribute("aria-label", "打开 Row 文档");
  link.append(element("strong", "", "打开 Row"),
    element("span", "", `revision ${locator.revision}`));
  return link;
}

function stepCard(row) {
  const card = element("article", "trace-step");
  const marker = element("div", "trace-step-marker", row.ordinal);
  const content = element("div", "trace-step-content");
  const header = element("div", "trace-step-heading");
  const operation = element("strong", "", row.operation);
  const outcome = element("span", "trace-outcome", row.outcome);
  outcome.dataset.outcome = row.outcome;
  header.append(operation, outcome);
  content.append(header, element("small", "",
    `${row.elapsed_ms} ms · remaining budget ${row.remaining_budget}`));
  if (row.parent_route_id) {
    const scope = element("div", "trace-route-line");
    scope.append(element("span", "", "parent"),
      routeLink(row.database_id, row.table_id, row.parent_route_id));
    content.append(scope);
  }
  if (row.candidate_route_ids.length) {
    const candidates = element("div", "trace-candidates");
    candidates.append(element("span", "", "candidates"));
    const list = element("div", "trace-chip-list");
    for (const id of row.candidate_route_ids) {
      const link = routeLink(row.database_id, row.table_id, id, "trace-chip");
      if (id === row.selected_route_id) link.dataset.selected = "true";
      list.append(link);
    }
    candidates.append(list);
    content.append(candidates);
  } else if (row.selected_route_id) {
    const selected = element("div", "trace-route-line");
    selected.append(element("span", "", "selected"),
      routeLink(row.database_id, row.table_id, row.selected_route_id));
    content.append(selected);
  }
  if (row.locators.length) {
    const locators = element("div", "trace-locator-list");
    for (const locator of row.locators) {
      locators.append(locatorLink(row.database_id, row.table_id, locator));
    }
    content.append(locators);
  }
  card.append(marker, content);
  return card;
}

async function loadDatabaseRoot(executeMSQL, cursor = "") {
  const source = cursor ? "SHOW DATABASES CURSOR :cursor LIMIT 32 COMPACT" :
    "SHOW DATABASES LIMIT 32 COMPACT";
  const inputs = cursor ? [statementInput({ cursor })] : [];
  const result = resultsFrom(await executeMSQL(source, inputs), 1)[0];
  return { rows: validateDatabaseRows(result.rows), page: validatePage(result, DATABASE_LIMIT) };
}

async function loadTimeline(executeMSQL, databaseID) {
  const database = quoteDatabase(databaseID);
  const source = `DESCRIBE DATABASE ${database} COMPACT; ` +
    `SHOW ROUTE TRACES IN DATABASE ${database} LIMIT 20`;
  const results = resultsFrom(await executeMSQL(source), 2);
  return {
    database: pointDatabase(results[0], databaseID),
    timeline: validateTimeline(results[1], databaseID)
  };
}

async function loadTimelinePage(executeMSQL, databaseID, cursor) {
  const source = `SHOW ROUTE TRACES IN DATABASE ${quoteDatabase(databaseID)} ` +
    "CURSOR :cursor LIMIT 20";
  const result = resultsFrom(await executeMSQL(source, [statementInput({ cursor })]), 1)[0];
  return validateTimeline(result, databaseID);
}

async function loadTrace(executeMSQL, databaseID, tableID, selectedTraceID) {
  const database = quoteDatabase(databaseID);
  const source = `DESCRIBE DATABASE ${database} COMPACT; ` +
    `SHOW ROUTE TRACE :trace IN DATABASE ${database} LIMIT 24`;
  const results = resultsFrom(await executeMSQL(source, [{}, statementInput({ trace: selectedTraceID })]), 2);
  return {
    database: pointDatabase(results[0], databaseID),
    steps: validateSteps(results[1], databaseID, tableID, selectedTraceID)
  };
}

async function loadStepPage(executeMSQL, databaseID, tableID, selectedTraceID, cursor) {
  const source = `SHOW ROUTE TRACE :trace IN DATABASE ${quoteDatabase(databaseID)} ` +
    "CURSOR :cursor LIMIT 24";
  const result = resultsFrom(await executeMSQL(source,
    [statementInput({ trace: selectedTraceID, cursor })]), 1)[0];
  return validateSteps(result, databaseID, tableID, selectedTraceID);
}

function markReady(section) {
  const root = section.closest(".route-outlet");
  if (root) root.dataset.pageState = "ready";
}

function addDatabaseContinuation(section, list, rows, page, executeMSQL) {
  const button = element("button", "load-more", "加载更多 Database");
  button.type = "button";
  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      const next = await loadDatabaseRoot(executeMSQL, page.next_cursor);
      if (next.page.snapshot !== page.snapshot) {
        throw new TraceViewError("revision_conflict", "Database snapshot changed");
      }
      const seen = new Set(rows.map((row) => row.database_id));
      for (const row of next.rows) {
        if (seen.has(row.database_id)) throw new TraceViewError("corrupt", "Database page duplicated an identity");
        seen.add(row.database_id);
        rows.push(row);
        list.append(databaseCard(row));
      }
      button.remove();
      if (next.page.truncated) addDatabaseContinuation(section, list, rows, next.page, executeMSQL);
      else markReady(section);
    } catch (error) {
      const [kind, title, detail] = errorState(error);
      section.append(stateNode(kind, title, detail));
      button.disabled = false;
    }
  });
  section.append(button);
}

function databaseSection(rows, page, executeMSQL) {
  const section = element("section", "catalog-section");
  const header = element("div", "section-heading");
  header.append(element("h3", "", "Databases"));
  const list = element("div", "object-grid");
  for (const row of rows) list.append(databaseCard(row));
  section.append(header, list);
  if (page.truncated) addDatabaseContinuation(section, list, rows, page, executeMSQL);
  return section;
}

function addTimelineContinuation(section, list, rows, page, databaseID, executeMSQL) {
  const button = element("button", "load-more", "加载后续 Trace");
  button.type = "button";
  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      const next = await loadTimelinePage(executeMSQL, databaseID, page.next_cursor);
      if (next.page.snapshot !== page.snapshot) {
        throw new TraceViewError("revision_conflict", "Trace timeline snapshot changed");
      }
      const seen = new Set(rows.map((row) => row.trace_id));
      if (rows.length && next.rows.length && next.rows[0].trace_sequence <= rows[rows.length - 1].trace_sequence) {
        throw new TraceViewError("corrupt", "Trace timeline continuation order is invalid");
      }
      for (const row of next.rows) {
        if (seen.has(row.trace_id)) throw new TraceViewError("corrupt", "Trace timeline duplicated a receipt");
        seen.add(row.trace_id);
        rows.push(row);
        list.append(traceCard(row));
      }
      button.remove();
      if (next.page.truncated) addTimelineContinuation(section, list, rows, next.page, databaseID, executeMSQL);
      else markReady(section);
    } catch (error) {
      const [kind, title, detail] = errorState(error);
      section.append(stateNode(kind, title, detail));
      button.disabled = false;
    }
  });
  section.append(button);
}

function timelineSection(timeline, databaseID, executeMSQL) {
  const section = element("section", "catalog-section trace-timeline");
  const header = element("div", "section-heading");
  header.append(element("h3", "", "Route Traces"));
  const list = element("div", "trace-list");
  for (const row of timeline.rows) list.append(traceCard(row));
  section.append(header, list);
  if (timeline.page.truncated) {
    addTimelineContinuation(section, list, timeline.rows, timeline.page, databaseID, executeMSQL);
  }
  return section;
}

function addStepContinuation(section, list, rows, page, databaseID, tableID, selectedTraceID, executeMSQL) {
  const button = element("button", "load-more", "加载后续 Step");
  button.type = "button";
  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      const next = await loadStepPage(executeMSQL, databaseID, tableID, selectedTraceID, page.next_cursor);
      if (next.page.snapshot !== page.snapshot) {
        throw new TraceViewError("revision_conflict", "Trace step snapshot changed");
      }
      if (rows.length && next.rows.length && next.rows[0].ordinal !== rows[rows.length - 1].ordinal + 1) {
        throw new TraceViewError("corrupt", "Trace step continuation order is invalid");
      }
      for (const row of next.rows) {
        rows.push(row);
        list.append(stepCard(row));
      }
      button.remove();
      if (next.page.truncated) {
        addStepContinuation(section, list, rows, next.page,
          databaseID, tableID, selectedTraceID, executeMSQL);
      } else {
        markReady(section);
      }
    } catch (error) {
      const [kind, title, detail] = errorState(error);
      section.append(stateNode(kind, title, detail));
      button.disabled = false;
    }
  });
  section.append(button);
}

function stepSection(steps, databaseID, tableID, selectedTraceID, executeMSQL) {
  const section = element("section", "catalog-section trace-flow");
  const header = element("div", "section-heading");
  header.append(element("h3", "", "Navigation steps"));
  const list = element("div", "trace-step-list");
  for (const row of steps.rows) list.append(stepCard(row));
  section.append(header, list);
  if (steps.page.truncated) {
    addStepContinuation(section, list, steps.rows, steps.page,
      databaseID, tableID, selectedTraceID, executeMSQL);
  }
  return section;
}

export async function renderTraces(root, options) {
  showState(root, "loading", "正在读取 Route Trace", "固定当前 Database、retention epoch 与读取预算…");
  try {
    const parts = pathParts(options.path);
    const view = element("div", "catalog-view trace-view");
    if (parts.length === 0) {
      const data = await loadDatabaseRoot(options.executeMSQL);
      if (!options.isCurrent()) return;
      if (data.rows.length === 0) {
        showState(root, "empty", "还没有可见 Database", "当前 scope 内没有可浏览的 Route Trace。");
        return;
      }
      view.append(breadcrumbs(), heading("Route Traces", "选择一个 Database 查看短期观察收据。"),
        databaseSection(data.rows, data.page, options.executeMSQL));
      root.dataset.pageState = data.page.truncated ? "truncated" : "ready";
      root.replaceChildren(view);
      return;
    }
    const databaseID = stableID(parts[0], "db_", "Database");
    if (parts.length === 1) {
      const data = await loadTimeline(options.executeMSQL, databaseID);
      if (!options.isCurrent()) return;
      view.append(breadcrumbs(data.database), heading(data.database.name, data.database.purpose, data.database));
      if (data.timeline.rows.length === 0) {
        view.append(stateNode("empty", "还没有导航记录",
          "当前没有 Agent 向 Memora 上报 Route Trace；接入 Agent 或 Hook 后会显示选择、回退、耗时和预算。"));
        root.dataset.pageState = "empty";
        root.replaceChildren(view);
        return;
      }
      view.append(timelineSection(data.timeline, databaseID, options.executeMSQL));
      root.dataset.pageState = data.timeline.page.truncated ? "truncated" : "ready";
      root.replaceChildren(view);
      return;
    }
    const tableID = stableID(parts[1], "tbl_", "Table");
    const selectedTraceID = traceID(parts[2]);
    const data = await loadTrace(options.executeMSQL, databaseID, tableID, selectedTraceID);
    if (!options.isCurrent()) return;
    if (data.steps.rows.length === 0) throw new TraceViewError("corrupt", "Trace has no steps");
    view.append(breadcrumbs(data.database, selectedTraceID),
      heading(`Trace ${data.steps.sequence}`, selectedTraceID, data.database, tableID),
      stepSection(data.steps, databaseID, tableID, selectedTraceID, options.executeMSQL));
    root.dataset.pageState = data.steps.page.truncated ? "truncated" : "ready";
    root.replaceChildren(view);
  } catch (error) {
    if (!options.isCurrent()) return;
    const [kind, title, detail] = errorState(error);
    showState(root, kind, title, detail);
  }
}
