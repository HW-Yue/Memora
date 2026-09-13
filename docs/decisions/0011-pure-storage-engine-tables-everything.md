# ADR-0011：存储引擎只做数据库，其余一切在它上面建表

状态：Accepted，2026-09-13。取代 [ADR-0004](./0004-fast-row-directory-minimal-mvcc.md)
中仍有效的 MVCC 部分与 [MVCC、Undo 与 Redo 边界](../storage/mvcc-undo-redo.md)。
语义树的表形态见 [Route 配套表](../product/route-companion-table.md)。

## 背景

现役引擎里除了「表」，还长着一批专用结构：版本树（`versions`）与按表的
`history:<tableID>` 树、objects 树（Route／Relation／Catalog 正文）、change 树、
全文树。每一种都有自己的 codec、generation 版本和发布路径。

后果有两个：一是每加一个产品概念就要新造一种存储，改动量落在最难改的那层；
二是版本树同时兼着产品历史（`SHOW HISTORY`、`AS OF`）和事务快照读，
两份职责绑死，谁都挪不动。

## 决定

### 1. 两层，边界只有「表」

```text
产品层   history、语义树、变更日志、Relation、配置……全部是表
         Agent 的每个操作 = 内部生成的一个事务，对这些表做 SQL 读写
───────────────────────────────────────────────
存储引擎 表、行、RowID、B+ 树、事务、WAL、恢复、Catalog
         不知道什么是 history，也不知道什么是 Route
```

引擎对所有表一视同仁。某张表是数据表、history 表还是语义表，
是产品层记在 Catalog 上的标记，引擎不据此改变行为。

### 2. 不做 MVCC

- 写事务串行执行；
- 读只看最新已提交状态，没有快照、没有 read view、没有 undo log；
- 未提交的写对任何读都不可见——提交点是树的 group commit，之前什么都没落；
- 分页读跨越多次请求时，可能看到页与页之间发生的提交。按 RowID 游标翻页，
  不重复不遗漏已存在的行，但不承诺整批读是同一时刻的切面。

理由：Memora 是单用户个人库，并发写极少；快照隔离的代价
（版本链、清理、可见性判断）远大于它在这里带来的价值。

### 3. history 是数据表的配套普通表

- 只有数据表配 history 表；语义表、history 表自己不配；
- Agent 改数据 → 一个事务内：改 `notes`，追加 `notes_history`；
- `SHOW HISTORY`、`AS OF` 改为对 history 表的 `SELECT`，不再依赖引擎版本树；
- 「只写数据表的 history」是产品层写路径的规则，引擎不需要「跳过 history」标志。
- history 只记**原地修改**；拆分、合并、删除改变身份，走主表里的废弃 + 接替，
  引用懒更新，见[数据行的生命周期](../product/row-lifecycle-successor.md)。

### 4. 语义树是配套普通表

Agent 改语义树 → 内部生成参数化 SQL，在一个事务内改 `notes_routes`
（以及需要时业务行上的 `route_leaf_ids`）。父子两面一致、扇出上限、同名检查、
废弃节点与接替者，由产品层在发出写之前守住。细节见 [Route 配套表](../product/route-companion-table.md)。

### 5. 其余专用结构依次归位

| 现役结构 | 归属 |
|---|---|
| objects 树里的 Relation | 产品层普通表 |
| change 树（谁改的、为什么） | 产品层普通表，与数据、history 同一事务写 |
| Configuration（`route_policy` 等） | 产品层普通表 |
| Catalog | 引擎系统表；表的角色标记是它的字段 |
| 版本树、`history:<tableID>` 树 | 删除（history 表取代，且不再有 MVCC） |
| 全文索引树 | **Deferred**：放引擎还是产品层，后面再定 |

对 Agent 可见哪些表（只有数据表），由产品层按 Catalog 标记过滤。

## 后果

- 新产品概念只需「建表 + 写 SQL」，不再触及 codec 与 generation 格式；
- 专用树逐个退役：objects 树、版本树、`history:` 树、change 树；
  每退一个，删掉它的 codec、发布路径和 generation 分支；
- 失去多语句读一致性；依赖快照的现役代码（`Authority.Capture()`、
  `IndexedReader.visibleLocator`、`AS OF COMMIT_SEQUENCE` 的可见性判断）要改写或删除；
- 老库如何升级到新形态，按每一步单独决定，不在本 ADR 承诺。

## 实施顺序（方向性，逐项开 Feature）

1. Catalog 支持表的角色标记；建数据表时自动建 `<t>_routes`，记 `router_root_id`；
2. Route 切到语义表，删除 `nativerouter` 与 objects 树里的 Route；
3. history 切到 history 表，删除版本树、`history:` 树与 MVCC 可见性代码；
4. Relation、change log、Configuration 依次表化，objects 树与 change 树退役；
5. 全文索引归属另行决定。

第 2 步不依赖第 3 步，可以最先开工。
