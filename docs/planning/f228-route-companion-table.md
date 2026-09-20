# F228：建数据表时自动建出隐藏的语义配套表

状态：**实现计划**（2026-09-13），待授权开工。
[ADR-0011](../decisions/0011-pure-storage-engine-tables-everything.md) 实施顺序第 1 步；
表形态见 [Route 配套表](../product/route-companion-table.md)。

## 一句话

`CREATE TABLE notes` 之后，Catalog 里同时有一张角色为「语义表」、对 Agent 不可见的
普通表，它有自己的表树，内部代码能对它读写行。

## 本 Feature 覆盖

1. **表角色**：`catalog.Table` 加 `Role`（`data`／`routes`）与 `OwnerTableID`
   （语义表指向它服务的数据表）；数据表加 `RouterRootID`（本步恒为空，第 2 步填）。
   Table codec 追加字段 15／16／17，缺省解码为 `data`、空，老 Catalog 照常读。
2. **同生**：`sqlstore` 在**同一个 SQLite 事务**里追加语义表。
   列：`name` `kind` `purpose` `synopsis` `parent_id` `row_id` 为 TEXT，
   `aliases` `child_ids` `successor_ids` 为 TEXT 存 JSON，`deprecated` 为 BOOLEAN。
   崩在建表中途时整笔回滚，下次 `CREATE TABLE` 再试。
3. **同死**：`ArchiveTable` 在同一次发布里归档其语义表。改名不联动（见命名）。
4. **命名**：语义表名 `_memora_routes_<ownerTableID>`，按 ID 不按名字派生，
   数据表改名无需联动；用户 `CREATE TABLE`／`RENAME TABLE` 拒绝 `_memora_` 前缀。
5. **不可见**：Agent 面一律看不到 `Role != data` 的表——
   `SHOW TABLES`、`DESCRIBE`、Catalog Atlas、Bootstrap、lexical 的 Catalog 文档；
   binder 解析 `SELECT／INSERT／UPDATE／DELETE` 的表名时报 not found（与不存在同一错误）。
6. **内部入口**：executor 增加进程内专用的 internal scope，只有它能绑定非 data 表；
   wire 协议与 Agent 会话无法设置该 scope。第 2 步的「内部拼 SQL」走这里。
7. **补建**：老库里没有语义表的数据表，在下一次任意 Catalog 发布时补建（幂等）；
   Catalog 校验只对「补建之后」的状态要求一表一伴。

## 不覆盖（相邻行为）

- 根节点行、`RouterRootID` 赋值、任何 Route 语义与 `SHOW ROUTES` 切换 → 第 2 步；
- 语义表不写 history → 第 3 步（本步没有行，无影响）；
- 删除旧 Route 专用引擎包（已在 `rewrite/adr0011` 完成，现役是 `sqlstore` 语义表）；
- 全文索引是否收录语义表 → Deferred。

## 协议与格式

- Catalog 角色字段缺省解码为 `data`；老库补建语义表，不升级自研 generation 格式；
- 不改 MSQL 语法；Agent 可观察的唯一变化是 `_memora_` 前缀被保留。

## RED

| 测试 | 命令 | 当前为什么失败 |
|---|---|---|
| `TestCreateTableCreatesHiddenRouteCompanion` | `go test ./internal/sqlstore -run Companion` | `CreateTable` 只产出一张表，`Table` 无 `Role` |

输入：新建库 → `CREATE TABLE notes`。期望：Catalog 快照里恰有两张表，
第二张 `Role=routes`、`OwnerTableID=notes.ID`、列集合如上；reopen 后不变。

## Failure matrix

| # | 场景 | 期望 |
|---|---|---|
| 1 | codec：带新字段的 Table 往返 | 字节 golden 一致，字段逐一相等 |
| 2 | codec：老 Table 正文（无 15–17） | 解码为 `Role=data`，不报损坏 |
| 3 | codec：`Role` 为未知值 | `ErrCorrupt` |
| 4 | Catalog 校验：语义表的 owner 不存在 | `ErrCorrupt` |
| 5 | Catalog 校验：一张数据表两张语义表 | `ErrCorrupt` |
| 6 | Catalog 校验：语义表的 owner 本身是语义表 | `ErrCorrupt`（不递归） |
| 7 | 故障：建数据表后、建语义表前崩溃 | 整笔回滚；reopen 两张表都不存在 |
| 8 | 故障：Catalog 发布本身在 group commit 前失败 | 两张表都不存在，无半张 |
| 9 | reopen：建表 → 关库 → 开库 | 语义表、表树、角色全部还在 |
| 10 | 归档数据表 | 同一次发布里语义表也归档；reopen 一致 |
| 11 | 老库补建：造一个无语义表的 Catalog，触发任意 Catalog 发布 | 补建出语义表；重复发布不再新增 |
| 12 | 可见性：`SHOW TABLES`／`DESCRIBE`／Atlas／Bootstrap／lexical | 均不含语义表 |
| 13 | 可见性：Agent 会话 `SELECT * FROM _memora_routes_…` 与 `INSERT` | 与表不存在同一错误码 |
| 14 | 保留前缀：`CREATE TABLE _memora_x`、`RENAME TABLE … TO _memora_x` | 结构化拒绝 |
| 15 | internal scope：插入并按 row_id 点查一行 | 成功；reopen 后读回 |
| 16 | internal scope 无法从 wire 请求设置 | 请求中携带同名字段被忽略或拒绝 |

## 改动量估算

| 部分 | 生产 | 测试 |
|---|---|---|
| `catalog` 模型 + `sqlstore` 校验 | ~90 | ~150 |
| 同生／同死／补建／保留前缀 | ~120 | ~180 |
| Agent 面过滤（executor、binder、atlas、lexical） | ~100 | ~150 |
| internal scope | ~60 | ~80 |
| **合计** | **~370** | **~560** |

## 合入门

`go test ./...`、`go test -race ./...`、`go vet ./...`、golden 检查，
加上表中 7、8、9 的 reopen 与故障注入证据。
