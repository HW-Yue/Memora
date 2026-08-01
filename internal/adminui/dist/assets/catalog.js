const PAGE_LIMIT = 32;

class CatalogViewError extends Error {
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

function quoteIdentifier(value, prefix) {
  if (typeof value !== "string" || value.length > 200 || !value.startsWith(prefix) ||
      !/^[A-Za-z0-9_-]+$/.test(value)) {
    throw new CatalogViewError("corrupt", "Catalog stable ID is invalid");
  }
  return `"${value.replaceAll('"', '""')}"`;
}

function pathParts(path) {
  const raw = path.split("/").filter(Boolean);
  if (raw[0] !== "catalog" || raw.length > 3) throw new CatalogViewError("corrupt", "Catalog route is invalid");
  try {
    return raw.slice(1).map(decodeURIComponent);
  } catch (_) {
    throw new CatalogViewError("corrupt", "Catalog route encoding is invalid");
  }
}

function firstFailure(envelope) {
  if (envelope && envelope.error) return envelope.error;
  if (envelope && Array.isArray(envelope.results)) {
    const failed = envelope.results.find((result) => result && result.error);
    if (failed) return failed.error;
  }
  return { code: "corrupt", message: "Result envelope is invalid" };
}

function resultsFrom(envelope, count) {
  if (!envelope || envelope.version !== "memora.result/v1" || !Array.isArray(envelope.results)) {
    throw new CatalogViewError("corrupt", "Result envelope version is invalid");
  }
  if (!envelope.ok) {
    const failure = firstFailure(envelope);
    throw new CatalogViewError(failure.code || "corrupt", "Catalog request failed");
  }
  if (envelope.results.length !== count || envelope.results.some((result) =>
    !result || result.status !== "succeeded" || !Array.isArray(result.rows))) {
    throw new CatalogViewError("corrupt", "Catalog result shape is invalid");
  }
  return envelope.results;
}

function validatePage(result) {
  const page = result.page;
  if (!page || page.version !== "memora.list-page/v1" || page.limit !== PAGE_LIMIT ||
      typeof page.cursor !== "string" || !/^sha256:[a-f0-9]{64}$/.test(page.snapshot) ||
      typeof page.truncated !== "boolean" ||
      (page.truncated && (typeof page.next_cursor !== "string" || page.next_cursor.length === 0))) {
    throw new CatalogViewError("corrupt", "Catalog page contract is invalid");
  }
  return page;
}

function isNonEmptyText(value) {
  return typeof value === "string" && value.length > 0;
}

function isSchemaVersion(value) {
  return Number.isSafeInteger(value) && value > 0;
}

function validateRows(rows, kind, databaseID = "") {
  const idName = kind === "database" ? "database_id" : kind === "table" ? "table_id" : "column_id";
  const prefix = kind === "database" ? "db_" : kind === "table" ? "tbl_" : "col_";
  for (const row of rows) {
    if (!row || Array.isArray(row) || !isNonEmptyText(row.name) || !isNonEmptyText(row.purpose)) {
      throw new CatalogViewError("corrupt", `Catalog ${kind} row is invalid`);
    }
    quoteIdentifier(row[idName], prefix);
    if (kind === "database" && (!isNonEmptyText(row.scope) || !isSchemaVersion(row.schema_version))) {
      throw new CatalogViewError("corrupt", "Catalog database fields are invalid");
    }
    if (kind === "table" &&
        (!isNonEmptyText(row.database_id) || (databaseID && row.database_id !== databaseID) ||
         !isNonEmptyText(row.row_semantics) || !isSchemaVersion(row.schema_version))) {
      throw new CatalogViewError("corrupt", "Catalog table fields are invalid");
    }
    if (kind === "column" &&
        (!isNonEmptyText(row.type) || typeof row.nullable !== "boolean" ||
         (row.max_characters !== undefined &&
          (!Number.isSafeInteger(row.max_characters) || row.max_characters <= 0)) ||
         (row.semantic_role !== undefined && typeof row.semantic_role !== "string") ||
         !isSchemaVersion(row.schema_version))) {
      throw new CatalogViewError("corrupt", `Catalog ${kind} row is invalid`);
    }
  }
  return rows;
}

function pointRow(result, kind, expectedID, databaseID = "") {
  const rows = validateRows(result.rows, kind, databaseID);
  if (rows.length !== 1 || rows[0][`${kind}_id`] !== expectedID) {
    throw new CatalogViewError("corrupt", `Catalog ${kind} point result is invalid`);
  }
  return rows[0];
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
    return ["permission", "无权查看这个范围", "当前 Admin session 未授权该 Database。"];
  }
  if (error.code === "revision_conflict") {
    return ["error", "Catalog 已发生变化", "当前分页快照已失效，请刷新这一层后继续。"];
  }
  if (error.code === "corrupt" || error.code === "internal_error") {
    return ["corrupt", "Catalog 响应无法验证", "页面拒绝展示不完整或不符合协议的数据。"];
  }
  return ["error", "暂时无法读取 Catalog", "请确认 daemon 正常运行后重试。"];
}

function breadcrumbs(parts) {
  const node = element("nav", "breadcrumbs");
  node.setAttribute("aria-label", "Catalog 路径");
  const root = element("a", "", "Instance");
  root.href = "/catalog";
  root.dataset.route = "";
  node.append(root);
  for (const part of parts) {
    const label = part.href ? element("a", "", part.label) : element("span", "", part.label);
    if (part.href) {
      label.href = part.href;
      label.dataset.route = "";
    }
    node.append(element("span", "", "/"), label);
  }
  return node;
}

function heading(title, purpose, id, version, extra) {
  const wrapper = element("header", "catalog-heading");
  const text = element("div");
  text.append(element("h2", "", title), element("p", "", purpose));
  const meta = element("div", "catalog-meta");
  if (id) meta.append(element("span", "object-id", id));
  if (version !== undefined) meta.append(element("span", "schema-badge", `schema v${version}`));
  if (extra) meta.append(element("span", "schema-badge", extra));
  text.append(meta);
  wrapper.append(text);
  return wrapper;
}

function objectCard(row, href, kind) {
  const card = element("a", "object-card");
  card.href = href;
  card.dataset.route = "";
  card.append(element("strong", "", row.name));
  const detail = kind === "table" ? row.row_semantics || row.purpose : row.purpose;
  card.append(element("p", "", detail), element("code", "", row[`${kind}_id`]));
  return card;
}

function columnRow(row) {
  const wrapper = element("div", "column-row");
  const identity = element("div");
  identity.append(element("strong", "", row.name), element("small", "", row.column_id));
  let type = row.type;
  if (row.max_characters) type += `(${row.max_characters})`;
  if (row.nullable === false) type += " · required";
  if (row.semantic_role) type += ` · ${row.semantic_role}`;
  wrapper.append(identity, element("div", "column-type", type), element("div", "column-purpose", row.purpose));
  return wrapper;
}

function markPaginationComplete(section) {
  const root = section.closest(".route-outlet");
  if (root) root.dataset.pageState = "ready";
}

function listSection(title, kind, rows, hrefFor, page, loadMore) {
  const section = element("section", "catalog-section");
  const sectionHeading = element("div", "section-heading");
  sectionHeading.append(element("h3", "", title), element("span", "", `snapshot ${page.snapshot.slice(0, 18)}…`));
  section.append(sectionHeading);
  const list = element("div", kind === "column" ? "column-list" : "object-grid");
  for (const row of rows) {
    list.append(kind === "column" ? columnRow(row) : objectCard(row, hrefFor(row), kind));
  }
  section.append(list);
  if (page.truncated) {
    const button = element("button", "load-more", "加载下一页");
    button.type = "button";
    button.addEventListener("click", async () => {
      button.disabled = true;
      try {
        const next = await loadMore(page.next_cursor);
        if (next.page.snapshot !== page.snapshot) {
          throw new CatalogViewError("revision_conflict", "Catalog page snapshot changed");
        }
        for (const row of next.rows) {
          list.append(kind === "column" ? columnRow(row) : objectCard(row, hrefFor(row), kind));
          rows.push(row);
        }
        button.remove();
        if (next.page.truncated) {
          section.append(listSectionContinuation(kind, rows, hrefFor, next.page, loadMore));
        } else {
          markPaginationComplete(section);
        }
      } catch (error) {
        const [state, stateTitle, detail] = errorState(error);
        section.append(stateNode(state, stateTitle, detail));
        button.disabled = false;
      }
    });
    section.append(button);
  }
  return section;
}

function listSectionContinuation(kind, rows, hrefFor, page, loadMore) {
  const button = element("button", "load-more", "继续加载");
  button.type = "button";
  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      const next = await loadMore(page.next_cursor);
      if (next.page.snapshot !== page.snapshot) {
        throw new CatalogViewError("revision_conflict", "Catalog page snapshot changed");
      }
      const parent = button.parentElement;
      const list = parent.querySelector(kind === "column" ? ".column-list" : ".object-grid");
      for (const row of next.rows) {
        list.append(kind === "column" ? columnRow(row) : objectCard(row, hrefFor(row), kind));
        rows.push(row);
      }
      button.remove();
      if (next.page.truncated) {
        parent.append(listSectionContinuation(kind, rows, hrefFor, next.page, loadMore));
      } else {
        markPaginationComplete(parent);
      }
    } catch (error) {
      const [state, stateTitle, detail] = errorState(error);
      button.parentElement.append(stateNode(state, stateTitle, detail));
      button.disabled = false;
    }
  });
  return button;
}

async function loadRoot(executeMSQL, cursor = "") {
  const source = cursor ? "SHOW DATABASES CURSOR :cursor LIMIT 32 COMPACT" : "SHOW DATABASES LIMIT 32 COMPACT";
  const inputs = cursor ? [{ parameters: { named: { cursor } } }] : [];
  const result = resultsFrom(await executeMSQL(source, inputs), 1)[0];
  return { rows: validateRows(result.rows, "database"), page: validatePage(result) };
}

async function loadDatabase(executeMSQL, databaseID) {
  const database = quoteIdentifier(databaseID, "db_");
  const source = `DESCRIBE DATABASE ${database} COMPACT; SHOW TABLES FROM ${database} LIMIT 32 COMPACT`;
  const results = resultsFrom(await executeMSQL(source), 2);
  return {
    object: pointRow(results[0], "database", databaseID),
    rows: validateRows(results[1].rows, "table", databaseID),
    page: validatePage(results[1])
  };
}

async function loadDatabasePage(executeMSQL, databaseID, cursor) {
  const database = quoteIdentifier(databaseID, "db_");
  const source = `SHOW TABLES FROM ${database} CURSOR :cursor LIMIT 32 COMPACT`;
  const result = resultsFrom(await executeMSQL(source, [{ parameters: { named: { cursor } } }]), 1)[0];
  return { rows: validateRows(result.rows, "table", databaseID), page: validatePage(result) };
}

async function loadTable(executeMSQL, databaseID, tableID) {
  const database = quoteIdentifier(databaseID, "db_");
  const table = quoteIdentifier(tableID, "tbl_");
  const subject = `${database}.${table}`;
  const source = `DESCRIBE TABLE ${subject} COMPACT; SHOW COLUMNS FROM ${subject} LIMIT 32 COMPACT`;
  const results = resultsFrom(await executeMSQL(source), 2);
  return {
    object: pointRow(results[0], "table", tableID, databaseID),
    rows: validateRows(results[1].rows, "column"),
    page: validatePage(results[1])
  };
}

async function loadTablePage(executeMSQL, databaseID, tableID, cursor) {
  const subject = `${quoteIdentifier(databaseID, "db_")}.${quoteIdentifier(tableID, "tbl_")}`;
  const source = `SHOW COLUMNS FROM ${subject} CURSOR :cursor LIMIT 32 COMPACT`;
  const result = resultsFrom(await executeMSQL(source, [{ parameters: { named: { cursor } } }]), 1)[0];
  return { rows: validateRows(result.rows, "column"), page: validatePage(result) };
}

export async function renderCatalog(root, options) {
  showState(root, "loading", "正在读取 Catalog", "只加载当前层和最多 32 个子对象…");
  try {
    const parts = pathParts(options.path);
    const view = element("div", "catalog-view");
    if (parts.length === 0) {
      const page = await loadRoot(options.executeMSQL);
      if (!options.isCurrent()) return;
      view.append(breadcrumbs([]), heading("Local Instance", "当前授权范围内的语义数据库。"));
      if (page.rows.length === 0) {
        showState(root, "empty", "还没有可见 Database", "当前 scope 内没有可浏览的 Database。");
        return;
      }
      view.append(listSection("Databases", "database", page.rows,
        (row) => `/catalog/${encodeURIComponent(row.database_id)}`, page.page,
        (cursor) => loadRoot(options.executeMSQL, cursor)));
      root.dataset.pageState = page.page.truncated ? "truncated" : "ready";
      root.replaceChildren(view);
      return;
    }
    if (parts.length === 1) {
      const data = await loadDatabase(options.executeMSQL, parts[0]);
      if (!options.isCurrent()) return;
      if (!data.object) throw new CatalogViewError("corrupt", "Database point result is empty");
      view.append(breadcrumbs([{ label: data.object.name }]), heading(data.object.name, data.object.purpose,
        data.object.database_id, data.object.schema_version, data.object.scope));
      if (data.rows.length === 0) {
        view.append(stateNode("empty", "这个 Database 还没有 Table", "Schema 建模后，Table 会显示在这里。"));
      } else {
        view.append(listSection("Tables", "table", data.rows,
          (row) => `/catalog/${encodeURIComponent(data.object.database_id)}/${encodeURIComponent(row.table_id)}`,
          data.page, (cursor) => loadDatabasePage(options.executeMSQL, data.object.database_id, cursor)));
      }
      root.dataset.pageState = data.rows.length === 0 ? "empty" : data.page.truncated ? "truncated" : "ready";
      root.replaceChildren(view);
      return;
    }
    const data = await loadTable(options.executeMSQL, parts[0], parts[1]);
    if (!options.isCurrent()) return;
    if (!data.object) throw new CatalogViewError("corrupt", "Table point result is empty");
    view.append(breadcrumbs([
      { label: data.object.database_id, href: `/catalog/${encodeURIComponent(data.object.database_id)}` },
      { label: data.object.name }
    ]), heading(data.object.name,
      data.object.purpose, data.object.table_id, data.object.schema_version, data.object.row_semantics));
    if (data.rows.length === 0) {
      view.append(stateNode("empty", "这个 Table 还没有 Column", "Schema Column 会显示在这里。"));
    } else {
      view.append(listSection("Schema Columns", "column", data.rows, () => "", data.page,
        (cursor) => loadTablePage(options.executeMSQL, data.object.database_id, data.object.table_id, cursor)));
    }
    root.dataset.pageState = data.rows.length === 0 ? "empty" : data.page.truncated ? "truncated" : "ready";
    root.replaceChildren(view);
  } catch (error) {
    if (!options.isCurrent()) return;
    const [state, title, detail] = errorState(error);
    showState(root, state, title, detail);
  }
}
