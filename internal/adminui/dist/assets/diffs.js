const MAX_BODY_BYTES = 512 * 1024;
const SYSTEM_COLUMNS = ["row_id", "revision", "commit_sequence", "row_state", "schema_version"];

class DiffViewError extends Error {
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
    throw new DiffViewError("corrupt", `${label} stable ID is invalid`);
  }
  return value;
}

function positiveRevision(value, label) {
  if (typeof value !== "string" || !/^[1-9][0-9]*$/.test(value)) {
    throw new DiffViewError("corrupt", `${label} revision is invalid`);
  }
  const revision = Number(value);
  if (!Number.isSafeInteger(revision)) throw new DiffViewError("corrupt", `${label} revision is too large`);
  return revision;
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
  if (raw[0] !== "diffs" || raw.length !== 6) {
    throw new DiffViewError("corrupt", "Row revision diff path is invalid");
  }
  try {
    return raw.slice(1).map(decodeURIComponent);
  } catch (_) {
    throw new DiffViewError("corrupt", "Row revision diff path encoding is invalid");
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

function resultsFrom(envelope) {
  if (!envelope || envelope.version !== "memora.result/v1" || !Array.isArray(envelope.results)) {
    throw new DiffViewError("corrupt", "Diff result envelope is invalid");
  }
  if (!envelope.ok) {
    const failure = firstFailure(envelope);
    throw new DiffViewError(failure.code || "corrupt", "Revision point read failed");
  }
  if (envelope.results.length !== 2 || envelope.results.some((result) =>
    !result || result.status !== "succeeded" || !Array.isArray(result.rows) ||
    !Array.isArray(result.columns))) {
    throw new DiffViewError("corrupt", "Diff result shape is invalid");
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

function validateColumns(columns) {
  if (columns.length < SYSTEM_COLUMNS.length) {
    throw new DiffViewError("corrupt", "Diff columns are incomplete");
  }
  const seen = new Set();
  for (let index = 0; index < columns.length; index += 1) {
    const column = columns[index];
    const allowed = ["name", "type", "nullable", "column_id", "purpose", "semantic_role"];
    if (!column || Object.keys(column).some((key) => !allowed.includes(key)) ||
        typeof column.name !== "string" || column.name.length === 0 ||
        typeof column.type !== "string" || column.type.length === 0 ||
        typeof column.nullable !== "boolean" || seen.has(column.name)) {
      throw new DiffViewError("corrupt", "Diff column metadata is invalid");
    }
    seen.add(column.name);
    if (index < SYSTEM_COLUMNS.length) {
      if (column.name !== SYSTEM_COLUMNS[index] || column.column_id !== undefined ||
          column.purpose !== undefined || column.semantic_role !== undefined) {
        throw new DiffViewError("corrupt", "Diff system columns are invalid");
      }
    } else {
      stableID(column.column_id, "col_", "Column");
      if (typeof column.purpose !== "string" || column.purpose.length === 0 ||
          (column.semantic_role !== undefined &&
           !["title", "summary", "identity", "status"].includes(column.semantic_role))) {
        throw new DiffViewError("corrupt", "Diff business column metadata is invalid");
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
      !positiveInteger(detail.schema_version) || typeof detail.row_semantics !== "string" ||
      detail.row_semantics.length === 0 || !detail.display || Array.isArray(detail.display) ||
      Object.keys(detail.display).some((key) =>
        !["title_column", "summary_column", "fallback"].includes(key))) {
    throw new DiffViewError("corrupt", "Diff Row Detail contract is invalid");
  }
  const byName = new Map(columns.map((column) => [column.name, column]));
  const title = detail.display.title_column || "";
  const summary = detail.display.summary_column || "";
  const fallback = detail.display.fallback || "";
  if ((title && byName.get(title)?.semantic_role !== "title") ||
      (summary && byName.get(summary)?.semantic_role !== "summary") ||
      (!title && fallback !== "row_id_revision") || (title && fallback)) {
    throw new DiffViewError("corrupt", "Diff display roles are invalid");
  }
  return detail;
}

function scalar(value) {
  return value === null || typeof value === "string" || typeof value === "boolean" ||
    (typeof value === "number" && Number.isFinite(value));
}

function validateSnapshot(result, databaseID, tableID, rowID, revision) {
  const columns = validateColumns(result.columns);
  const detail = validateDetail(result, databaseID, tableID, columns);
  if (result.rows.length !== 1) throw new DiffViewError("corrupt", "Revision point read is not exact");
  const row = result.rows[0];
  if (!exactKeys(row, columns.map((column) => column.name)) || row.row_id !== rowID ||
      row.revision !== revision || !positiveInteger(row.commit_sequence) ||
      !["live", "deleted", "superseded"].includes(row.row_state) ||
      !positiveInteger(row.schema_version)) {
    throw new DiffViewError("corrupt", "Revision identity or state is invalid");
  }
  for (const column of columns.slice(SYSTEM_COLUMNS.length)) {
    if (!scalar(row[column.name])) throw new DiffViewError("corrupt", "Revision contains a non-scalar value");
  }
  return { row, columns, detail };
}

function sameContract(before, after) {
  return JSON.stringify(before.columns) === JSON.stringify(after.columns) &&
    JSON.stringify(before.detail) === JSON.stringify(after.detail);
}

function enforceBodyBudget(before, after) {
  const encoded = JSON.stringify([before.row, after.row]);
  if (new TextEncoder().encode(encoded).byteLength > MAX_BODY_BYTES) {
    throw new DiffViewError("over_budget", "Revision pair exceeds the body budget");
  }
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
    return ["empty", "找不到指定 revision", "Row 或其中一个历史 revision 不存在。"];
  }
  if (error.code === "permission_denied") {
    return ["permission", "无权比较这个 Row", "当前 Admin session 未授权该 Database。"];
  }
  if (error.code === "over_budget") {
    return ["over_budget", "revision pair 超出展示预算", "两侧正文合计超过 512 KiB，页面拒绝截断差异。"];
  }
  if (error.code === "corrupt" || error.code === "internal_error") {
    return ["corrupt", "Diff 响应无法验证", "页面拒绝比较错 scope、错 revision 或不完整字段。"];
  }
  return ["error", "暂时无法比较 revision", "请确认 daemon 正常运行后重试。"];
}

function displayValue(value) {
  if (value === null) return "null";
  return String(value);
}

function breadcrumbs(databaseID, tableID, rowID) {
  const node = element("nav", "breadcrumbs");
  node.setAttribute("aria-label", "Revision diff 路径");
  const row = element("a", "", rowID);
  row.href = `/rows/${encodeURIComponent(databaseID)}/${encodeURIComponent(tableID)}/${encodeURIComponent(rowID)}`;
  row.dataset.route = "";
  node.append(element("span", "", "Row"), element("span", "", "/"), row,
    element("span", "", "/"), element("span", "", "Diff"));
  return node;
}

function titleFor(snapshot, rowID) {
  const titleColumn = snapshot.detail.display.title_column;
  return titleColumn ? displayValue(snapshot.row[titleColumn]) : `${rowID} · revision ${snapshot.row.revision}`;
}

function heading(before, after, rowID) {
  const wrapper = element("header", "catalog-heading diff-heading");
  const content = element("div");
  content.append(element("h2", "", titleFor(after, rowID)),
    element("p", "", before.detail.row_semantics));
  const meta = element("div", "catalog-meta");
  meta.append(element("span", "object-id", rowID),
    element("span", "schema-badge", `revision ${before.row.revision} → ${after.row.revision}`),
    element("span", "schema-badge", `${before.row.row_state} → ${after.row.row_state}`));
  content.append(meta);
  wrapper.append(content);
  return wrapper;
}

function valuesEqual(left, right) {
  return Object.is(left, right);
}

function valueSide(label, snapshot, column) {
  const side = element("div", "diff-side");
  const heading = element("div", "diff-side-heading");
  heading.append(element("strong", "", label),
    element("span", "", `revision ${snapshot.row.revision}`));
  side.append(heading, element("pre", "diff-value", displayValue(snapshot.row[column.name])));
  return side;
}

function fieldDiff(column, before, after) {
  const changed = !valuesEqual(before.row[column.name], after.row[column.name]);
  const section = element("section", "diff-field");
  section.dataset.changed = changed ? "true" : "false";
  const header = element("div", "row-field-heading");
  const identity = element("div");
  identity.append(element("h3", "", column.name), element("small", "", column.column_id));
  const badges = element("div", "row-field-badges");
  badges.append(element("span", "schema-badge", column.type),
    element("span", "schema-badge", changed ? "changed" : "unchanged"));
  if (column.semantic_role) badges.append(element("span", "schema-badge", column.semantic_role));
  header.append(identity, badges);
  const values = element("div", "diff-values");
  values.append(valueSide("Before", before, column), valueSide("After", after, column));
  section.append(header, element("p", "row-field-purpose", column.purpose), values);
  return section;
}

function diffDocument(before, after) {
  const business = before.columns.slice(SYSTEM_COLUMNS.length);
  const changed = business.filter((column) =>
    !valuesEqual(before.row[column.name], after.row[column.name])).length;
  const section = element("section", "catalog-section diff-document");
  const summary = element("div", "section-heading");
  summary.append(element("h3", "", `${changed} changed fields`),
    element("span", "", `${business.length - changed} unchanged`));
  const fields = element("div", "diff-field-list");
  for (const column of business) fields.append(fieldDiff(column, before, after));
  section.append(summary, fields);
  return section;
}

async function loadDiff(executeMSQL, databaseID, tableID, rowID, beforeRevision, afterRevision) {
  const subject = `${quoteIdentifier(databaseID, "db_")}.${quoteIdentifier(tableID, "tbl_")}`;
  const source = `SELECT * FROM ${subject} AS OF REVISION :before WHERE row_id = :row LIMIT 1; ` +
    `SELECT * FROM ${subject} AS OF REVISION :after WHERE row_id = :row LIMIT 1`;
  const results = resultsFrom(await executeMSQL(source, [
    statementInput({ row: rowID, before: beforeRevision }),
    statementInput({ row: rowID, after: afterRevision })
  ]));
  const before = validateSnapshot(results[0], databaseID, tableID, rowID, beforeRevision);
  const after = validateSnapshot(results[1], databaseID, tableID, rowID, afterRevision);
  if (!sameContract(before, after)) throw new DiffViewError("corrupt", "Revision contracts do not match");
  enforceBodyBudget(before, after);
  return { before, after };
}

export async function renderDiff(root, options) {
  showState(root, "loading", "正在读取两个 Row revision", "执行两次有界 AS OF point read…");
  try {
    const parts = pathParts(options.path);
    const databaseID = stableID(parts[0], "db_", "Database");
    const tableID = stableID(parts[1], "tbl_", "Table");
    const rowID = stableID(parts[2], "row_", "Row");
    const beforeRevision = positiveRevision(parts[3], "Before");
    const afterRevision = positiveRevision(parts[4], "After");
    if (beforeRevision >= afterRevision) {
      throw new DiffViewError("corrupt", "Revision pair order is invalid");
    }
    const data = await loadDiff(options.executeMSQL, databaseID, tableID, rowID,
      beforeRevision, afterRevision);
    if (!options.isCurrent()) return;
    const view = element("div", "catalog-view diff-view");
    view.append(breadcrumbs(databaseID, tableID, rowID), heading(data.before, data.after, rowID),
      diffDocument(data.before, data.after));
    root.dataset.pageState = "ready";
    root.replaceChildren(view);
  } catch (error) {
    if (!options.isCurrent()) return;
    const [kind, title, detail] = errorState(error);
    showState(root, kind, title, detail);
  }
}
