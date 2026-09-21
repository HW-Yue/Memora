// The search page asks the semantic index a question in words.
//
// It runs the keyword recall arm and shows the positions it found, each one a
// link into the semantic tree at that exact leaf. Recall answers with positions
// and nothing else: no similarity, no distance, no ordering — so this page shows
// the path, the source it came from, and how many results the limit cut off. It
// never invents an order it cannot justify: with more than one Database in scope
// the results are interleaved, not sorted by a number nobody measured.
//
// The two arms are fused by the engine, not by this page: the gateway embeds the
// query with the host's provider, asks for one RECALL that runs both arms, and
// hands back the RRF listing untouched. The page therefore does not sort, and it
// says per Database whether the vector arm actually ran.

const SEARCH_LIMIT = 10;
const DATABASE_LIMIT = 32;
const TABLE_LIMIT = 64;

class SearchViewError extends Error {
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

function statementInput(named) {
  return { parameters: { named } };
}

function resultsFrom(envelope, count) {
  if (!envelope || envelope.version !== "memora.result/v1" || !Array.isArray(envelope.results)) {
    throw new SearchViewError("corrupt", "Result envelope version is invalid");
  }
  if (!envelope.ok) {
    const failure = envelope.error || (envelope.results.find((item) => item && item.error) || {}).error;
    throw new SearchViewError((failure && failure.code) || "corrupt", "Search request failed");
  }
  if (envelope.results.length !== count || envelope.results.some((result) =>
    !result || result.status !== "succeeded" || !Array.isArray(result.rows))) {
    throw new SearchViewError("corrupt", "Search result shape is invalid");
  }
  return envelope.results;
}

function isText(value) {
  return typeof value === "string" && value.length > 0;
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
    return ["permission", "无权在全部范围内搜索", "当前 Admin session 只授权了一部分 Database。"];
  }
  if (error.code === "revision_conflict") {
    return ["error", "目录已发生变化", "请刷新这一页后重新搜索。"];
  }
  if (error.code === "corrupt" || error.code === "internal_error") {
    return ["corrupt", "搜索响应无法验证", "页面拒绝展示不完整或不符合协议的结果。"];
  }
  if (error.code === "validation_error" || error.code === "unsupported_statement") {
    return ["error", "这个查询无法回答", "关键词召回需要至少 3 个字符；更短的查询会被拒绝而不是返回空结果。"];
  }
  return ["error", "搜索暂时不可用", "请确认 daemon 正常运行后重试。"];
}

async function loadDatabases(executeMSQL) {
  const result = resultsFrom(await executeMSQL(
    `SHOW DATABASES LIMIT ${DATABASE_LIMIT} COMPACT`, []), 1)[0];
  const rows = [];
  for (const row of result.rows) {
    if (!row || !isText(row.name) || !isText(row.database_id) || !isText(row.purpose)) {
      throw new SearchViewError("corrupt", "Database row is invalid");
    }
    rows.push({ name: row.name, database_id: row.database_id, purpose: row.purpose });
  }
  return rows;
}

async function loadTables(executeMSQL, databaseID) {
  const quoted = `"${databaseID.replaceAll('"', '""')}"`;
  const result = resultsFrom(await executeMSQL(
    `SHOW TABLES FROM ${quoted} LIMIT ${TABLE_LIMIT} COMPACT`, []), 1)[0];
  const byName = new Map();
  for (const row of result.rows) {
    if (!row || !isText(row.name) || !isText(row.table_id)) {
      throw new SearchViewError("corrupt", "Table row is invalid");
    }
    byName.set(row.name, row.table_id);
  }
  return byName;
}

function validateHits(result) {
  const hits = [];
  for (const row of result.rows) {
    if (!row || !isText(row.database) || !isText(row.table) || !isText(row.kind) ||
        !Array.isArray(row.path) || row.path.length === 0) {
      throw new SearchViewError("corrupt", "Recall row is invalid");
    }
    const path = [];
    for (const segment of row.path) {
      if (!segment || !isText(segment.name) || !isText(segment.route_id)) {
        throw new SearchViewError("corrupt", "Recall path segment is invalid");
      }
      path.push({ name: segment.name, route_id: segment.route_id });
    }
    const objectID = row.object_id === null || row.object_id === undefined ? "" : row.object_id;
    if (objectID !== "" && typeof objectID !== "string") {
      throw new SearchViewError("corrupt", "Recall object id is invalid");
    }
    hits.push({ database: row.database, table: row.table, kind: row.kind, path, objectID });
  }
  return hits;
}

// The gateway's search receipt: the same positions a RECALL returns, plus what
// the vector arm did and the exact statement that was run.
function validateReceipt(payload, databaseName) {
  if (!payload || payload.version !== "memora.admin-search/v1" || payload.database !== databaseName ||
      typeof payload.source !== "string" || payload.source.length === 0 ||
      !payload.vector || typeof payload.vector.ran !== "boolean" ||
      !Array.isArray(payload.rows)) {
    throw new SearchViewError("corrupt", "Search receipt is invalid");
  }
  return payload;
}

async function searchDatabase(search, databaseName, query) {
  return validateReceipt(await search(databaseName, query), databaseName);
}

function vectorNote(databaseName, vector) {
  if (vector.ran) return null;
  switch (vector.reason) {
    case "not_configured":
      return "本机没有配置 MEMORA_EMBEDDING_*：整页只走关键词路，配好之后这一页会自动带上向量。";
    case "embedding_timeout":
      return `${databaseName}：向量路这次超时了（${vector.detail || "provider timeout"}）；` +
        "结果仍然完整地来自关键词路，重试一次就能带上向量。";
    case "provider_unavailable":
      return `${databaseName}：向量路这次没答上（${vector.detail || "provider error"}）；` +
        "关键词结果照常，重试一次即可。";
    case "vector_not_ready":
      return `${databaseName}：这个库还不能回答向量查询（还没锁向量身份，或正在 rekey）：` +
        "这一库只走了关键词。";
    default:
      return `${databaseName}：向量路没有运行（${vector.reason || "unknown"}）。`;
  }
}

// Interleaving, not ordering. The two arms and the Databases are not comparable:
// recall carries nothing to compare them by, so the page round-robins them and
// says where each result came from instead of inventing a global order.
function interleave(groups, limit) {
  const merged = [];
  const seen = new Set();
  let round = 0;
  let added = true;
  while (added && merged.length < limit) {
    added = false;
    for (const group of groups) {
      const hit = group.hits[round];
      if (!hit) continue;
      added = true;
      const key = `${group.database}/${hit.table}/${hit.path[hit.path.length - 1].route_id}`;
      if (seen.has(key)) continue;
      seen.add(key);
      merged.push({ ...hit, source: group.source, vectorRan: group.vectorRan, databaseID: group.databaseID });
      if (merged.length >= limit) break;
    }
    round += 1;
  }
  return merged;
}

function pathLabel(hit) {
  return hit.path.map((segment) => segment.name).join(" / ");
}

function resultCard(hit, tableIDs) {
  // The label says which arms produced this Database's listing, because once the
  // engine has fused them there is no per-row provenance left to show — and there
  // never was a score to show instead.
  const leaf = hit.path[hit.path.length - 1];
  const tableID = tableIDs.get(hit.table);
  const card = element("article", "search-result");
  const link = element("a", "search-result-link");
  if (tableID) {
    link.href = `/routes/${encodeURIComponent(hit.databaseID)}/${encodeURIComponent(tableID)}/` +
      encodeURIComponent(leaf.route_id);
    link.dataset.route = "";
  } else {
    link.href = `/catalog/${encodeURIComponent(hit.databaseID)}`;
    link.dataset.route = "";
  }
  const heading = element("div", "search-result-heading");
  heading.append(element("strong", "", pathLabel(hit)));
  const badges = element("div", "search-result-badges");
  badges.append(element("span", "schema-badge", hit.database));
  badges.append(element("span", "schema-badge", hit.table));
  badges.append(element("span", "schema-badge search-source", hit.source));
  if (hit.vectorRan === undefined) throw new SearchViewError("corrupt", "Result source is unknown");
  heading.append(badges);
  link.append(heading);
  const meta = element("p", "search-result-meta", tableID
    ? "在语义索引里展开到这个叶子"
    : "这个 Table 没有稳定的 ID 可跳转，先进入 Catalog");
  link.append(meta);
  card.append(link);
  return card;
}

function resultSection(hits, tableIDs, truncatedDatabases, noticeLines) {
  const section = element("section", "catalog-section search-results");
  const heading = element("div", "section-heading");
  heading.append(element("h3", "", `前 ${hits.length} 个位置`));
  section.append(heading);
  const list = element("div", "search-result-list");
  for (const hit of hits) list.append(resultCard(hit, tableIDs));
  section.append(list);
  const notes = element("div", "search-notes");
  notes.append(element("p", "", "召回只回答「在哪」：结果是语义位置，不是事实；点进去之后回表读正文。"));
  notes.append(element("p", "", "同一个 Database 内是引擎按名次融合后的顺序（RRF），跨 Database 才是交错；" +
    "召回不返回分数、距离或名次，所以没有可展示的相似度。"));
  for (const line of noticeLines) notes.append(element("p", "", line));
  if (truncatedDatabases.length) {
    notes.append(element("p", "", `这些 Database 的命中被 LIMIT ${SEARCH_LIMIT} 截断：` +
      truncatedDatabases.join("、") + "。要更全就缩小范围或换更精确的词。"));
  }
  section.append(notes);
  return section;
}

function searchForm(databases, selected, query) {
  const form = element("form", "search-form");
  form.setAttribute("aria-label", "语义检索");
  const select = element("select", "search-select");
  select.name = "database";
  const all = element("option", "", "全部 Database");
  all.value = "";
  if (selected === "") all.selected = true;
  select.append(all);
  for (const database of databases) {
    const option = element("option", "", database.name);
    option.value = database.name;
    if (database.name === selected) option.selected = true;
    select.append(option);
  }
  const input = element("input", "search-input");
  input.type = "search";
  input.name = "query";
  input.placeholder = "用一句话描述你要找的东西（至少 3 个字符）";
  input.value = query;
  input.autocomplete = "off";
  input.minLength = 3;
  const button = element("button", "search-button", "搜索");
  button.type = "submit";
  form.append(select, input, button);
  return form;
}

function updateLocation(selected, query) {
  const parameters = new URLSearchParams();
  if (query) parameters.set("q", query);
  if (selected) parameters.set("db", selected);
  const suffix = parameters.toString();
  window.history.replaceState(null, "", suffix ? `/search?${suffix}` : "/search");
}

export async function renderSearch(root, options) {
  showState(root, "loading", "正在读取语义索引", "关键词与向量两路由引擎融合；只读取位置，不读取任何正文…");
  try {
    const parameters = new URLSearchParams(window.location.search);
    const selected = parameters.get("db") || "";
    const query = (parameters.get("q") || "").trim();
    const databases = await loadDatabases(options.executeMSQL);
    if (!options.isCurrent()) return;
    if (databases.length === 0) {
      showState(root, "empty", "还没有可见 Database", "当前 scope 内没有可搜索的内容。");
      return;
    }
    const view = element("div", "catalog-view search-view");
    view.append(element("header", "catalog-heading search-heading"));
    const heading = view.querySelector(".search-heading");
    const headingText = element("div");
    headingText.append(element("h2", "", "搜索"));
    headingText.append(element("p", "", "两路召回融合后的位置；结果点进去直接在语义索引里展开到那一片叶子。"));
    heading.append(headingText);
    view.append(searchForm(databases, selected, query));
    const form = view.querySelector(".search-form");
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      const nextSelected = form.querySelector(".search-select").value;
      const nextQuery = form.querySelector(".search-input").value.trim();
      updateLocation(nextSelected, nextQuery);
      renderSearch(root, options);
    });

    if (query.length < 3) {
      view.append(stateNode("empty", "输入至少 3 个字符",
        "trigram 索引需要 3 个字符起步；更短的查询会被引擎拒绝，而不是返回空结果。"));
      root.dataset.pageState = "empty";
      root.replaceChildren(view);
      return;
    }

    const scopes = selected === "" ? databases : databases.filter((item) => item.name === selected);
    if (scopes.length === 0) {
      view.append(stateNode("empty", "找不到这个 Database", "它可能已被重命名或不在当前授权范围内。"));
      root.dataset.pageState = "empty";
      root.replaceChildren(view);
      return;
    }

    const groups = [];
    const tableIDs = new Map();
    const truncated = [];
    const noticeLines = [];
    for (const scope of scopes) {
      const receipt = await searchDatabase(options.search, scope.name, query);
      if (!options.isCurrent()) return;
      const hits = validateHits({ rows: receipt.rows });
      groups.push({
        database: scope.name, databaseID: scope.database_id, hits,
        source: receipt.vector.ran ? "两臂融合（RRF）" : "关键词",
        vectorRan: receipt.vector.ran, source_statement: receipt.source,
      });
      if (receipt.truncated) truncated.push(scope.name);
      const note = vectorNote(scope.name, receipt.vector);
      if (note) noticeLines.push(note);
      for (const warning of receipt.warnings || []) {
        if (warning && warning.code === "vectors_not_ready") {
          noticeLines.push(`${scope.name}：向量索引还有未就绪的单元，这一库的列表可能不完整。`);
        }
      }
      noticeLines.push(`发给引擎的语句（${scope.name}）：${receipt.source}`);
      const tables = await loadTables(options.executeMSQL, scope.database_id);
      for (const [name, id] of tables) tableIDs.set(name, id);
    }
    const hits = interleave(groups, SEARCH_LIMIT);
    if (hits.length === 0) {
      view.append(stateNode("empty", "没有找到位置",
        "换个更具体的词，或者先确认这些知识已经写进了语义索引。"));
      root.dataset.pageState = "empty";
      root.replaceChildren(view);
      return;
    }
    view.append(resultSection(hits, tableIDs, truncated, noticeLines));
    root.dataset.pageState = "ready";
    root.replaceChildren(view);
  } catch (error) {
    if (!options.isCurrent()) return;
    const [kind, title, detail] = errorState(error);
    showState(root, kind, title, detail);
  }
}
