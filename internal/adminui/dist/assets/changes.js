const DATABASE_LIMIT = 32;
const TIMELINE_LIMIT = 20;
const ENTRY_LIMIT = 32;
const TIMELINE_COLUMNS = [
  ["version", "TEXT", false], ["transaction_id", "ID", false],
  ["commit_sequence", "INTEGER", false], ["committed_at", "TIMESTAMP", false],
  ["actor", "TEXT", false], ["source", "TEXT", false], ["reason", "TEXT", false],
  ["source_receipt_id", "TEXT", true], ["database_ids", "ID_LIST", false],
  ["entry_count", "INTEGER", false], ["checksum", "TEXT", false]
];
const ENTRY_COLUMNS = [
  ["transaction_id", "ID", false], ["commit_sequence", "INTEGER", false],
  ["object_kind", "TEXT", false], ["database_id", "ID", true],
  ["table_id", "ID", true], ["object_id", "ID", false],
  ["operation", "TEXT", false], ["before_revision", "INTEGER", true],
  ["after_revision", "INTEGER", false], ["schema_version", "INTEGER", true],
  ["history_locator", "ID", true], ["related_object_ids", "ID_LIST", false]
];
const OBJECT_KINDS = new Set([
  "database", "table", "column", "row", "relation", "route_node",
  "route_membership", "configuration"
]);
const OPERATIONS = new Set(["INSERT", "UPDATE", "DELETE", "COMPENSATE", "SPLIT", "MERGE", "RESTORE"]);

class ChangeViewError extends Error {
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
    throw new ChangeViewError("corrupt", `${label} stable ID is invalid`);
  }
  return value;
}

function safeText(value, label, optional = false, limit = 4096) {
  if (optional && (value === null || value === "")) return value;
  if (typeof value !== "string" || value.length === 0 || value.length > limit) {
    throw new ChangeViewError("corrupt", `${label} text is invalid`);
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
  if (raw[0] !== "changes" || raw.length > 2) {
    throw new ChangeViewError("corrupt", "Change timeline path is invalid");
  }
  try {
    return raw.slice(1).map(decodeURIComponent);
  } catch (_) {
    throw new ChangeViewError("corrupt", "Change timeline path encoding is invalid");
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
    throw new ChangeViewError("corrupt", "Change result envelope is invalid");
  }
  if (!envelope.ok) {
    const failure = firstFailure(envelope);
    throw new ChangeViewError(failure.code || "corrupt", "Change request failed");
  }
  if (envelope.results.length !== count || envelope.results.some((result) =>
    !result || result.status !== "succeeded" || !Array.isArray(result.rows) ||
    !Array.isArray(result.columns))) {
    throw new ChangeViewError("corrupt", "Change result shape is invalid");
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

function validateColumns(columns, expected) {
  if (columns.length !== expected.length) {
    throw new ChangeViewError("corrupt", "Change columns are incomplete");
  }
  for (let index = 0; index < expected.length; index += 1) {
    const column = columns[index];
    const [name, type, nullable] = expected[index];
    if (!exactKeys(column, ["name", "type", "nullable"]) || column.name !== name ||
        column.type !== type || column.nullable !== nullable) {
      throw new ChangeViewError("corrupt", "Change columns do not match the frozen contract");
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
    throw new ChangeViewError("corrupt", "Change page contract is invalid");
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
    if (!exactKeys(row, fields)) throw new ChangeViewError("corrupt", "Database summary is invalid");
    stableID(row.database_id, "db_", "Database");
    safeText(row.name, "Database name", false, 256);
    safeText(row.purpose, "Database purpose");
    safeText(row.scope, "Database scope");
    if (row.anti_scope !== undefined) safeText(row.anti_scope, "Database anti-scope");
    if (!positiveInteger(row.schema_version) || !Array.isArray(row.aliases) ||
        !Array.isArray(row.tables) || row.tables.length !== 0 ||
        !validTimestamp(row.created_at) || !validTimestamp(row.updated_at) ||
        seen.has(row.database_id)) {
      throw new ChangeViewError("corrupt", "Database summary identity is invalid");
    }
    const aliases = new Set();
    for (const alias of row.aliases) {
      safeText(alias, "Database alias", false, 256);
      if (aliases.has(alias)) throw new ChangeViewError("corrupt", "Database aliases are duplicated");
      aliases.add(alias);
    }
    seen.add(row.database_id);
  }
  return rows;
}

function pointDatabase(result, databaseID) {
  const rows = validateDatabaseRows(result.rows);
  if (rows.length !== 1 || rows[0].database_id !== databaseID) {
    throw new ChangeViewError("corrupt", "Database point response is invalid");
  }
  return rows[0];
}

function validTimestamp(value) {
  return typeof value === "string" && value.length <= 64 && Number.isFinite(Date.parse(value));
}

function validateTimeline(result, databaseID) {
  validateColumns(result.columns, TIMELINE_COLUMNS);
  const fields = TIMELINE_COLUMNS.map((column) => column[0]);
  const seen = new Set();
  let previous = 0;
  for (const row of result.rows) {
    if (!exactKeys(row, fields) || row.version !== "memora.committed-change/v1" ||
        typeof row.transaction_id !== "string" || !/^txn_[a-f0-9]{32}$/.test(row.transaction_id) ||
        !positiveInteger(row.commit_sequence) || row.commit_sequence <= previous ||
        !validTimestamp(row.committed_at) || !safeText(row.actor, "Actor", false, 256) ||
        !safeText(row.source, "Source", false, 256) || !safeText(row.reason, "Reason") ||
        (row.source_receipt_id !== null && row.source_receipt_id !== "" &&
         !safeText(row.source_receipt_id, "Source receipt")) ||
        !Array.isArray(row.database_ids) || row.database_ids.length !== 1 ||
        row.database_ids[0] !== databaseID || !positiveInteger(row.entry_count) ||
        row.entry_count > 4096 || typeof row.checksum !== "string" ||
        !/^[a-f0-9]{64}$/.test(row.checksum) ||
        row.transaction_id !== `txn_${row.checksum.slice(0, 32)}` || seen.has(row.transaction_id)) {
      throw new ChangeViewError("corrupt", "Change timeline row is invalid");
    }
    previous = row.commit_sequence;
    seen.add(row.transaction_id);
  }
  return { rows: result.rows, page: validatePage(result, TIMELINE_LIMIT) };
}

function entryKey(row) {
  return [row.object_kind, row.database_id, row.table_id, row.object_id,
    String(row.after_revision).padStart(20, "0")].join("\u0000");
}

function validateEntries(result, databaseID, summary) {
  validateColumns(result.columns, ENTRY_COLUMNS);
  const fields = ENTRY_COLUMNS.map((column) => column[0]);
  const seen = new Set();
  let previous = "";
  for (const row of result.rows) {
    if (!exactKeys(row, fields) || row.transaction_id !== summary.transaction_id ||
        row.commit_sequence !== summary.commit_sequence || !OBJECT_KINDS.has(row.object_kind) ||
        row.database_id !== databaseID || typeof row.table_id !== "string" ||
        !safeText(row.object_id, "Changed object") || !OPERATIONS.has(row.operation) ||
        !nonnegativeInteger(row.before_revision) || !positiveInteger(row.after_revision) ||
        row.before_revision >= row.after_revision || !nonnegativeInteger(row.schema_version) ||
        typeof row.history_locator !== "string" || !Array.isArray(row.related_object_ids) ||
        row.related_object_ids.length > 256) {
      throw new ChangeViewError("corrupt", "Change entry is invalid");
    }
    if (row.table_id) stableID(row.table_id, "tbl_", "Table");
    if (row.history_locator) safeText(row.history_locator, "History locator");
    let relatedPrevious = "";
    for (const related of row.related_object_ids) {
      safeText(related, "Related object");
      if (related <= relatedPrevious) throw new ChangeViewError("corrupt", "Related object order is invalid");
      relatedPrevious = related;
    }
    const key = entryKey(row);
    if ((previous && key <= previous) || seen.has(key)) {
      throw new ChangeViewError("corrupt", "Change entry order is invalid");
    }
    previous = key;
    seen.add(key);
  }
  return { rows: result.rows, page: validatePage(result, ENTRY_LIMIT) };
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
    return ["permission", "无权查看变化时间线", "当前 Admin session 未授权该 Database。"];
  }
  if (error.code === "revision_conflict" || error.code === "validation_error") {
    return ["revision_conflict", "变化快照无法继续", "cursor 已失效或不属于当前 Database/事务，请刷新页面。"];
  }
  if (error.code === "corrupt" || error.code === "internal_error") {
    return ["corrupt", "Change 响应无法验证", "页面拒绝展示不完整、跨 scope 或乱序的事务数据。"];
  }
  return ["error", "暂时无法读取变化", "请确认 daemon 正常运行后重试。"];
}

function breadcrumbs(database) {
  const node = element("nav", "breadcrumbs");
  node.setAttribute("aria-label", "Changes 路径");
  const root = element("a", "", "Changes");
  root.href = "/changes";
  root.dataset.route = "";
  node.append(root);
  if (database) node.append(element("span", "", "/"), element("span", "", database.name));
  return node;
}

function heading(title, purpose, database) {
  const wrapper = element("header", "catalog-heading change-heading");
  const text = element("div");
  text.append(element("h2", "", title));
  if (purpose) text.append(element("p", "", purpose));
  wrapper.append(text);
  return wrapper;
}

function databaseCard(row) {
  const card = element("a", "object-card");
  card.href = `/changes/${encodeURIComponent(row.database_id)}`;
  card.dataset.route = "";
  card.append(element("strong", "", row.name), element("p", "", row.purpose));
  return card;
}

function operationBadge(operation) {
  const badge = element("span", "change-operation", operation);
  badge.dataset.operation = operation.toLowerCase();
  return badge;
}

function entryCard(row) {
  const card = element("article", "change-entry");
  const header = element("div", "change-entry-heading");
  const identity = element("div");
  identity.append(element("strong", "", row.object_kind));
  header.append(identity, operationBadge(row.operation));
  const revisions = row.before_revision > 0 ?
    `revision ${row.before_revision} → ${row.after_revision}` : `revision ${row.after_revision}`;
  card.append(header, element("p", "", revisions));
  if (row.object_kind === "row" && row.before_revision > 0) {
    const link = element("a", "revision-link", "比较 before / after");
    link.href = `/diffs/${encodeURIComponent(row.database_id)}/${encodeURIComponent(row.table_id)}/` +
      `${encodeURIComponent(row.object_id)}/${row.before_revision}/${row.after_revision}`;
    link.dataset.route = "";
    card.append(link);
  }
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
    `SHOW CHANGES IN DATABASE ${database} LIMIT 20`;
  const results = resultsFrom(await executeMSQL(source), 2);
  return {
    database: pointDatabase(results[0], databaseID),
    timeline: validateTimeline(results[1], databaseID)
  };
}

async function loadTimelinePage(executeMSQL, databaseID, cursor) {
  const source = `SHOW CHANGES IN DATABASE ${quoteDatabase(databaseID)} CURSOR :cursor LIMIT 20`;
  const result = resultsFrom(await executeMSQL(source, [statementInput({ cursor })]), 1)[0];
  return validateTimeline(result, databaseID);
}

async function loadEntryPage(executeMSQL, databaseID, summary, cursor = "") {
  const database = quoteDatabase(databaseID);
  const source = cursor ?
    `SHOW CHANGE :transaction IN DATABASE ${database} CURSOR :cursor LIMIT 32` :
    `SHOW CHANGE :transaction IN DATABASE ${database} LIMIT 32`;
  const named = cursor ? { transaction: summary.transaction_id, cursor } :
    { transaction: summary.transaction_id };
  const result = resultsFrom(await executeMSQL(source, [statementInput(named)]), 1)[0];
  return validateEntries(result, databaseID, summary);
}

function addEntryContinuation(container, list, rows, page, summary, loader) {
  const button = element("button", "load-more", "加载更多对象变化");
  button.type = "button";
  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      const next = await loader(page.next_cursor);
      if (next.page.snapshot !== page.snapshot) {
        throw new ChangeViewError("revision_conflict", "Entry snapshot changed");
      }
      const seen = new Set(rows.map(entryKey));
      if (rows.length && next.rows.length && entryKey(next.rows[0]) <= entryKey(rows[rows.length - 1])) {
        throw new ChangeViewError("corrupt", "Entry continuation order is invalid");
      }
      for (const row of next.rows) {
        if (seen.has(entryKey(row))) throw new ChangeViewError("corrupt", "Entry continuation duplicated an object");
        seen.add(entryKey(row));
        rows.push(row);
        list.append(entryCard(row));
      }
      button.remove();
      if (next.page.truncated) addEntryContinuation(container, list, rows, next.page, summary, loader);
      else if (rows.length !== summary.entry_count) {
        throw new ChangeViewError("corrupt", "Visible entry count does not match the transaction summary");
      }
    } catch (error) {
      const [kind, title, detail] = errorState(error);
      container.append(stateNode(kind, title, detail));
      button.disabled = false;
    }
  });
  container.append(button);
}

function changeCard(summary, databaseID, executeMSQL) {
  const card = element("article", "change-card");
  const header = element("div", "change-card-heading");
  const identity = element("div");
  identity.append(element("strong", "", `commit ${summary.commit_sequence}`),
    element("code", "", summary.transaction_id));
  header.append(identity, element("time", "", summary.committed_at));
  const reason = element("p", "change-reason", summary.reason);
  const metadata = element("small", "", `${summary.actor} · ${summary.source} · ${summary.entry_count} entries`);
  const expand = element("button", "change-expand", `展开 ${summary.entry_count} 个变化`);
  expand.type = "button";
  expand.addEventListener("click", async () => {
    expand.disabled = true;
    try {
      const entries = await loadEntryPage(executeMSQL, databaseID, summary);
      if (entries.rows.length === 0 || (!entries.page.truncated && entries.rows.length !== summary.entry_count)) {
        throw new ChangeViewError("corrupt", "Transaction entry count is invalid");
      }
      const container = element("section", "change-entries");
      const sectionHeading = element("div", "section-heading");
      sectionHeading.append(element("h4", "", "Changed objects"));
      const list = element("div", "change-entry-list");
      for (const row of entries.rows) list.append(entryCard(row));
      container.append(sectionHeading, list);
      if (entries.page.truncated) {
        addEntryContinuation(container, list, entries.rows, entries.page, summary,
          (cursor) => loadEntryPage(executeMSQL, databaseID, summary, cursor));
      }
      expand.replaceWith(container);
    } catch (error) {
      const [kind, title, detail] = errorState(error);
      card.append(stateNode(kind, title, detail));
      expand.disabled = false;
    }
  });
  card.append(header, reason, metadata, expand);
  return card;
}

function markReady(section) {
  const root = section.closest(".route-outlet");
  if (root) root.dataset.pageState = "ready";
}

function addTimelineContinuation(section, list, rows, page, databaseID, executeMSQL) {
  const button = element("button", "load-more", "加载后续事务");
  button.type = "button";
  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      const next = await loadTimelinePage(executeMSQL, databaseID, page.next_cursor);
      if (next.page.snapshot !== page.snapshot) {
        throw new ChangeViewError("revision_conflict", "Timeline snapshot changed");
      }
      const seen = new Set(rows.map((row) => row.transaction_id));
      if (rows.length && next.rows.length && next.rows[0].commit_sequence <= rows[rows.length - 1].commit_sequence) {
        throw new ChangeViewError("corrupt", "Timeline continuation order is invalid");
      }
      for (const row of next.rows) {
        if (seen.has(row.transaction_id)) throw new ChangeViewError("corrupt", "Timeline duplicated a transaction");
        seen.add(row.transaction_id);
        rows.push(row);
        list.append(changeCard(row, databaseID, executeMSQL));
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

function databaseSection(rows, page, executeMSQL) {
  const section = element("section", "catalog-section");
  const header = element("div", "section-heading");
  header.append(element("h3", "", "Databases"));
  const list = element("div", "object-grid");
  for (const row of rows) list.append(databaseCard(row));
  section.append(header, list);
  if (page.truncated) {
    const button = element("button", "load-more", "加载更多 Database");
    button.type = "button";
    button.addEventListener("click", async () => {
      button.disabled = true;
      try {
        const next = await loadDatabaseRoot(executeMSQL, page.next_cursor);
        if (next.page.snapshot !== page.snapshot) {
          throw new ChangeViewError("revision_conflict", "Database snapshot changed");
        }
        const seen = new Set(rows.map((row) => row.database_id));
        for (const row of next.rows) {
          if (seen.has(row.database_id)) throw new ChangeViewError("corrupt", "Database page duplicated an identity");
          seen.add(row.database_id);
          rows.push(row);
          list.append(databaseCard(row));
        }
        button.remove();
        if (next.page.truncated) section.append(databaseSectionContinuation(rows, list, next.page, executeMSQL));
        else markReady(section);
      } catch (error) {
        const [kind, title, detail] = errorState(error);
        section.append(stateNode(kind, title, detail));
        button.disabled = false;
      }
    });
    section.append(button);
  }
  return section;
}

function databaseSectionContinuation(rows, list, page, executeMSQL) {
  const button = element("button", "load-more", "继续加载 Database");
  button.type = "button";
  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      const next = await loadDatabaseRoot(executeMSQL, page.next_cursor);
      if (next.page.snapshot !== page.snapshot) {
        throw new ChangeViewError("revision_conflict", "Database snapshot changed");
      }
      const seen = new Set(rows.map((row) => row.database_id));
      for (const row of next.rows) {
        if (seen.has(row.database_id)) throw new ChangeViewError("corrupt", "Database page duplicated an identity");
        seen.add(row.database_id);
        rows.push(row);
        list.append(databaseCard(row));
      }
      const parent = button.parentElement;
      button.remove();
      if (next.page.truncated) parent.append(databaseSectionContinuation(rows, list, next.page, executeMSQL));
      else markReady(parent);
    } catch (error) {
      const [kind, title, detail] = errorState(error);
      button.parentElement.append(stateNode(kind, title, detail));
      button.disabled = false;
    }
  });
  return button;
}

export async function renderChanges(root, options) {
  showState(root, "loading", "正在读取 committed changes", "先固定当前 Database 与事务快照…");
  try {
    const parts = pathParts(options.path);
    const view = element("div", "catalog-view change-view");
    if (parts.length === 0) {
      const data = await loadDatabaseRoot(options.executeMSQL);
      if (!options.isCurrent()) return;
      if (data.rows.length === 0) {
        showState(root, "empty", "还没有可见 Database", "当前 scope 内没有可浏览的变化时间线。");
        return;
      }
      view.append(breadcrumbs(), heading("Committed Changes", "选择一个 Database 查看固定快照的事务时间线。"),
        databaseSection(data.rows, data.page, options.executeMSQL));
      root.dataset.pageState = data.page.truncated ? "truncated" : "ready";
      root.replaceChildren(view);
      return;
    }
    const databaseID = stableID(parts[0], "db_", "Database");
    const data = await loadTimeline(options.executeMSQL, databaseID);
    if (!options.isCurrent()) return;
    view.append(breadcrumbs(data.database), heading(data.database.name, data.database.purpose, data.database));
    if (data.timeline.rows.length === 0) {
      view.append(stateNode("empty", "还没有 committed change", "新的逻辑事务提交后会出现在这里。"));
      root.dataset.pageState = "empty";
      root.replaceChildren(view);
      return;
    }
    const section = element("section", "catalog-section change-timeline");
    const header = element("div", "section-heading");
    header.append(element("h3", "", "Transactions"));
    const list = element("div", "change-list");
    for (const row of data.timeline.rows) list.append(changeCard(row, databaseID, options.executeMSQL));
    section.append(header, list);
    if (data.timeline.page.truncated) {
      addTimelineContinuation(section, list, data.timeline.rows, data.timeline.page,
        databaseID, options.executeMSQL);
    }
    view.append(section);
    root.dataset.pageState = data.timeline.page.truncated ? "truncated" : "ready";
    root.replaceChildren(view);
  } catch (error) {
    if (!options.isCurrent()) return;
    const [kind, title, detail] = errorState(error);
    showState(root, kind, title, detail);
  }
}
