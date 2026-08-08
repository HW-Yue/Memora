const HISTORY_LIMIT = 20;
const SYSTEM_COLUMNS = ["row_id", "revision", "commit_sequence", "row_state", "schema_version"];
const HISTORY_FIELDS = [
  "row_id", "revision", "commit_sequence", "schema_version", "operation", "row_state",
  "actor", "source", "source_kind", "source_receipt_id", "source_locator",
  "source_content_hash", "reason", "updated_at"
];

class RowViewError extends Error {
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
    throw new RowViewError("corrupt", `${label} stable ID is invalid`);
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
  if (raw[0] !== "rows" || raw.length !== 4) {
    throw new RowViewError("corrupt", "Row document path is invalid");
  }
  try {
    return raw.slice(1).map(decodeURIComponent);
  } catch (_) {
    throw new RowViewError("corrupt", "Row document path encoding is invalid");
  }
}

function resultsFrom(envelope, count) {
  if (!envelope || envelope.version !== "memora.result/v1" || !Array.isArray(envelope.results)) {
    throw new RowViewError("corrupt", "Result envelope version is invalid");
  }
  if (!envelope.ok) {
    let failure = envelope.error;
    if (!failure) failure = envelope.results.find((item) => item && item.error)?.error;
    throw new RowViewError(failure?.code || "corrupt", "Row document request failed");
  }
  if (envelope.results.length !== count || envelope.results.some((result) =>
    !result || result.status !== "succeeded" || !Array.isArray(result.rows) ||
    !Array.isArray(result.columns))) {
    throw new RowViewError("corrupt", "Row document result shape is invalid");
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

function validatePage(result) {
  const page = result.page;
  if (!page || page.version !== "memora.list-page/v1" || page.limit !== HISTORY_LIMIT ||
      typeof page.cursor !== "string" || !/^sha256:[a-f0-9]{64}$/.test(page.snapshot) ||
      typeof page.truncated !== "boolean" ||
      (page.truncated && (typeof page.next_cursor !== "string" || page.next_cursor.length === 0))) {
    throw new RowViewError("corrupt", "History page contract is invalid");
  }
  return page;
}

function validateColumns(columns) {
  if (columns.length < SYSTEM_COLUMNS.length) {
    throw new RowViewError("corrupt", "Row columns are incomplete");
  }
  const names = new Set();
  for (let index = 0; index < columns.length; index += 1) {
    const column = columns[index];
    const allowed = ["name", "type", "nullable", "column_id", "purpose", "semantic_role"];
    if (!column || Object.keys(column).some((key) => !allowed.includes(key)) ||
        typeof column.name !== "string" || column.name.length === 0 ||
        typeof column.type !== "string" || column.type.length === 0 ||
        typeof column.nullable !== "boolean" || names.has(column.name)) {
      throw new RowViewError("corrupt", "Row column metadata is invalid");
    }
    names.add(column.name);
    if (index < SYSTEM_COLUMNS.length) {
      if (column.name !== SYSTEM_COLUMNS[index] || column.column_id !== undefined ||
          column.purpose !== undefined || column.semantic_role !== undefined) {
        throw new RowViewError("corrupt", "Row system columns are invalid");
      }
    } else {
      stableID(column.column_id, "col_", "Column");
      if (typeof column.purpose !== "string" || column.purpose.length === 0 ||
          (column.semantic_role !== undefined &&
           !["title", "summary", "identity", "status"].includes(column.semantic_role))) {
        throw new RowViewError("corrupt", "Row business column metadata is invalid");
      }
    }
  }
  return columns;
}

function validateDetail(result, databaseID, tableID, columns) {
  const detail = result.row_detail;
  const fields = [
    "version", "database_id", "database_name", "table_id", "table_name",
    "schema_version", "row_semantics", "display"
  ];
  if (!exactKeys(detail, fields) || detail.version !== "memora.row-detail/v1" ||
      detail.database_id !== databaseID || detail.table_id !== tableID ||
      typeof detail.database_name !== "string" || detail.database_name.length === 0 ||
      typeof detail.table_name !== "string" || detail.table_name.length === 0 ||
      !positiveInteger(detail.schema_version) ||
      typeof detail.row_semantics !== "string" || detail.row_semantics.length === 0 ||
      !detail.display || Array.isArray(detail.display) ||
      Object.keys(detail.display).some((key) => !["title_column", "summary_column", "fallback"].includes(key))) {
    throw new RowViewError("corrupt", "Row detail contract is invalid");
  }
  const title = detail.display.title_column || "";
  const summary = detail.display.summary_column || "";
  const fallback = detail.display.fallback || "";
  const byName = new Map(columns.map((column) => [column.name, column]));
  if ((title && byName.get(title)?.semantic_role !== "title") ||
      (summary && byName.get(summary)?.semantic_role !== "summary") ||
      (!title && fallback !== "row_id_revision") || (title && fallback)) {
    throw new RowViewError("corrupt", "Row display roles are invalid");
  }
  return detail;
}

function validateCurrent(result, databaseID, tableID, rowID) {
  const columns = validateColumns(result.columns);
  const detail = validateDetail(result, databaseID, tableID, columns);
  if (result.rows.length > 1) throw new RowViewError("corrupt", "Point Row returned multiple values");
  const row = result.rows[0];
  if (row) {
    if (!exactKeys(row, columns.map((column) => column.name)) || row.row_id !== rowID ||
        !positiveInteger(row.revision) || !positiveInteger(row.commit_sequence) ||
        row.row_state !== "live" || row.schema_version !== detail.schema_version) {
      throw new RowViewError("corrupt", "Point Row identity or revision is invalid");
    }
  }
  return { row, columns, detail };
}

function nullableText(value) {
  return value === null || typeof value === "string";
}

function validateHistory(result, rowID) {
  const seen = new Set();
  let previous = Number.MAX_SAFE_INTEGER;
  for (const row of result.rows) {
    if (!exactKeys(row, HISTORY_FIELDS) || row.row_id !== rowID ||
        !positiveInteger(row.revision) || !positiveInteger(row.commit_sequence) ||
        !positiveInteger(row.schema_version) || row.revision >= previous || seen.has(row.revision) ||
        typeof row.operation !== "string" || row.operation.length === 0 ||
        typeof row.row_state !== "string" || row.row_state.length === 0 ||
        typeof row.actor !== "string" || typeof row.source !== "string" ||
        typeof row.source_kind !== "string" || !nullableText(row.source_receipt_id) ||
        !nullableText(row.source_locator) || !nullableText(row.source_content_hash) ||
        typeof row.reason !== "string" || typeof row.updated_at !== "string") {
      throw new RowViewError("corrupt", "History metadata is invalid");
    }
    seen.add(row.revision);
    previous = row.revision;
  }
  return { rows: result.rows, page: validatePage(result) };
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
    return ["permission", "无权查看这个 Row", "当前 Admin session 未授权该 Database。"];
  }
  if (error.code === "revision_conflict") {
    return ["error", "Row History 已发生变化", "当前分页快照已失效，请刷新页面后继续。"];
  }
  if (error.code === "corrupt" || error.code === "internal_error") {
    return ["corrupt", "Row 响应无法验证", "页面拒绝展示不完整或跨 scope 的动态文档。"];
  }
  return ["error", "暂时无法读取 Row", "请确认 daemon 正常运行后重试。"];
}

function displayValue(value) {
  if (value === null) return "null";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  const encoded = JSON.stringify(value, null, 2);
  if (encoded === undefined) throw new RowViewError("corrupt", "Row value cannot be displayed");
  return encoded;
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

function markdownValue(value) {
  const wrapper = element("div", "markdown-value");
  const rendered = element("div", "markdown-body");
  const fragment = markdownFragment(value);
  if (fragment) rendered.append(fragment);
  else rendered.append(element("pre", "row-field-value", displayValue(value)));
  const source = element("pre", "row-field-value markdown-source");
  source.textContent = displayValue(value);
  source.hidden = true;
  const toggle = element("button", "markdown-toggle", "查看 Markdown 原文");
  toggle.type = "button";
  toggle.addEventListener("click", () => {
    const showingSource = !source.hidden;
    source.hidden = showingSource;
    rendered.hidden = !showingSource;
    toggle.textContent = showingSource ? "查看 Markdown 原文" : "查看渲染结果";
  });
  wrapper.append(rendered, source, toggle);
  return wrapper;
}

function heading(current, rowID) {
  const detail = current.detail;
  const titleColumn = detail.display.title_column;
  const title = current.row && titleColumn ? displayValue(current.row[titleColumn]) :
    current.row ? `${rowID} · revision ${current.row.revision}` : rowID;
  const wrapper = element("header", "catalog-heading row-heading");
  const content = element("div");
  content.append(element("h2", "", title), element("p", "", detail.row_semantics));
  const badges = element("div", "catalog-meta");
  badges.append(element("span", "object-id", rowID),
    element("span", "schema-badge", `schema v${detail.schema_version}`));
  if (current.row) {
    badges.append(element("span", "schema-badge", current.row.row_state),
      element("span", "schema-badge", `revision ${current.row.revision}`));
  }
  content.append(badges);
  if (current.row && detail.display.summary_column) {
    content.append(element("p", "row-summary", displayValue(current.row[detail.display.summary_column])));
  }
  wrapper.append(content);
  return wrapper;
}

function fieldSection(column, value) {
  const section = element("section", "row-field");
  const header = element("div", "row-field-heading");
  const identity = element("div");
  identity.append(element("h3", "", column.name), element("small", "", column.column_id));
  const badges = element("div", "row-field-badges");
  badges.append(element("span", "schema-badge", column.type));
  if (column.semantic_role) badges.append(element("span", "schema-badge", column.semantic_role));
  header.append(identity, badges);
  section.append(header, element("p", "row-field-purpose", column.purpose));
  if (typeof value === "string") section.append(markdownValue(value));
  else section.append(element("pre", "row-field-value", displayValue(value)));
  return section;
}

function documentSection(current) {
  const section = element("section", "row-document");
  for (const column of current.columns.slice(SYSTEM_COLUMNS.length)) {
    section.append(fieldSection(column, current.row[column.name]));
  }
  return section;
}

function historyCard(row, databaseID, tableID) {
  const card = element("article", "history-card");
  const header = element("div", "history-card-heading");
  header.append(element("strong", "", `revision ${row.revision}`),
    element("span", "", row.operation), element("time", "", row.updated_at));
  card.append(header, element("p", "", `${row.row_state} · commit ${row.commit_sequence} · schema v${row.schema_version}`));
  const provenance = [row.actor, row.source_kind, row.source, row.reason].filter((value) => value);
  if (provenance.length) card.append(element("small", "", provenance.join(" · ")));
  if (row.revision > 1) {
    const link = element("a", "revision-link", "与上一 revision 比较");
    link.href = `/diffs/${encodeURIComponent(databaseID)}/${encodeURIComponent(tableID)}/` +
      `${encodeURIComponent(row.row_id)}/${row.revision - 1}/${row.revision}`;
    link.dataset.route = "";
    card.append(link);
  }
  return card;
}

function markHistoryComplete(section, finalState) {
  const root = section.closest(".route-outlet");
  if (root) root.dataset.pageState = finalState;
}

function historySection(history, loadMore, finalState, databaseID, tableID) {
  const section = element("section", "catalog-section history-section");
  const header = element("div", "section-heading");
  header.append(element("h3", "", "History"),
    element("span", "", `snapshot ${history.page.snapshot.slice(0, 18)}…`));
  const list = element("div", "history-list");
  for (const row of history.rows) list.append(historyCard(row, databaseID, tableID));
  section.append(header, list);
  if (history.page.truncated) {
    addHistoryContinuation(section, list, history.rows, history.page, loadMore,
      finalState, databaseID, tableID);
  }
  return section;
}

function metadataPanel(current, rowID) {
  const panel = element("div", "row-side-metadata");
  panel.append(element("p", "inspector-kicker", "ROW METADATA"), element("h3", "", "当前记录"));
  panel.append(element("span", "object-id", rowID),
    element("span", "schema-badge", `schema v${current.detail.schema_version}`));
  if (current.row) {
    panel.append(element("span", "schema-badge", `revision ${current.row.revision}`),
      element("span", "schema-badge", current.row.row_state));
  }
  panel.append(element("p", "row-side-semantics", current.detail.row_semantics));
  return panel;
}

function addHistoryContinuation(section, list, rows, page, loadMore, finalState, databaseID, tableID) {
  const button = element("button", "load-more", "加载更早 revision");
  button.type = "button";
  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      const next = await loadMore(page.next_cursor);
      if (next.page.snapshot !== page.snapshot) {
        throw new RowViewError("revision_conflict", "History page snapshot changed");
      }
      if (rows.length && next.rows.length && next.rows[0].revision >= rows[rows.length - 1].revision) {
        throw new RowViewError("corrupt", "History continuation order is invalid");
      }
      const revisions = new Set(rows.map((row) => row.revision));
      for (const row of next.rows) {
        if (revisions.has(row.revision)) throw new RowViewError("corrupt", "History continuation duplicated a revision");
        revisions.add(row.revision);
        rows.push(row);
        list.append(historyCard(row, databaseID, tableID));
      }
      button.remove();
      if (next.page.truncated) {
        addHistoryContinuation(section, list, rows, next.page, loadMore,
          finalState, databaseID, tableID);
      }
      else markHistoryComplete(section, finalState);
    } catch (error) {
      const [kind, stateTitle, detail] = errorState(error);
      section.append(stateNode(kind, stateTitle, detail));
      button.disabled = false;
    }
  });
  section.append(button);
}

async function loadRow(executeMSQL, databaseID, tableID, rowID) {
  const subject = `${quoteIdentifier(databaseID, "db_")}.${quoteIdentifier(tableID, "tbl_")}`;
  const source = `SELECT * FROM ${subject} WHERE row_id = :row LIMIT 1; ` +
    `SHOW HISTORY FROM ${subject} FOR ROW :row LIMIT 20`;
  const input = statementInput({ row: rowID });
  const results = resultsFrom(await executeMSQL(source, [input, input]), 2);
  return {
    current: validateCurrent(results[0], databaseID, tableID, rowID),
    history: validateHistory(results[1], rowID)
  };
}

async function loadHistory(executeMSQL, databaseID, tableID, rowID, cursor) {
  const subject = `${quoteIdentifier(databaseID, "db_")}.${quoteIdentifier(tableID, "tbl_")}`;
  const source = `SHOW HISTORY FROM ${subject} FOR ROW :row CURSOR :cursor LIMIT 20`;
  const result = resultsFrom(await executeMSQL(source,
    [statementInput({ row: rowID, cursor })]), 1)[0];
  return validateHistory(result, rowID);
}

export async function renderRow(root, options) {
  showState(root, "loading", "正在读取 Row document", "按 RowID 回表并加载最多 20 条 History…");
  try {
    const parts = pathParts(options.path);
    const databaseID = stableID(parts[0], "db_", "Database");
    const tableID = stableID(parts[1], "tbl_", "Table");
    const rowID = stableID(parts[2], "row_", "Row");
    const data = await loadRow(options.executeMSQL, databaseID, tableID, rowID);
    if (!options.isCurrent()) return;
    const view = element("div", "catalog-view row-view");
    const layout = element("div", "row-document-layout");
    const paper = element("article", "row-document-paper");
    paper.append(heading(data.current, rowID));
    if (data.current.row) paper.append(documentSection(data.current));
    else paper.append(stateNode("empty", "当前 Row 不可见", "Row detail contract 有效，但当前 revision 没有 live value。"));
    const side = element("aside", "row-side-panel");
    side.append(metadataPanel(data.current, rowID));
    if (data.history.rows.length) {
      const finalState = data.current.row ? "ready" : "empty";
      side.append(historySection(data.history,
        (cursor) => loadHistory(options.executeMSQL, databaseID, tableID, rowID, cursor),
        finalState, databaseID, tableID));
    } else {
      side.append(stateNode("empty", "还没有 History", "这个 RowID 没有可见 revision metadata。"));
    }
    layout.append(paper, side);
    view.append(layout);
    root.dataset.pageState = data.history.page.truncated ? "truncated" : data.current.row ? "ready" : "empty";
    root.replaceChildren(view);
  } catch (error) {
    if (!options.isCurrent()) return;
    const [kind, title, detail] = errorState(error);
    showState(root, kind, title, detail);
  }
}
