# 数据行之间的双向链接

状态：**方向性结论**（2026-09-13）。
分层前提见 [ADR-0011](../decisions/0011-pure-storage-engine-tables-everything.md)；
链接遇到废弃行的处理见 [数据行的生命周期](./row-lifecycle-successor.md)。

## 一句话

链接是数据行上的一个字段：写明链到哪一行，并带上那一行的摘要。
不单独建关系表，也不是独立的对象。

## 字段

数据表的每一行带一个默认字段 `links`（与 `route_leaf_id` 同类，先存为 TEXT 里的 JSON）：

```text
links: [
  { row_id: 对方行的 RowID, summary: 对方行的摘要, revision: 摘要取自对方哪个 revision },
  ...
]
```

RowID 自带表空间，全局唯一，所以链接可以跨表，不必再写 table。

## 双向

A 链到 B 时，**一个事务内**同时写两面：

- A 的 `links` 加 `{ B, B 的摘要 }`；
- B 的 `links` 加 `{ A, A 的摘要 }`。

删除链接同样两面一起删。任何时候两面必须一致，不允许只有一面。

读一行时，`links` 里的摘要让 Agent 不回表就知道对面是什么；
要事实仍按 `row_id` 点查对方行。

## 与废弃行

对方行被拆分或合并后，链接按[生命周期](./row-lifecycle-successor.md)懒更新：
改指全部接替者，两面一起改，每个接替者的摘要取自接替者本身。

## 当前实现

行上已有 `links` 字段（`setLinks` / `RowLinks` / `summarize`）。
字段的 MSQL 读写待实现。

## 摘要懒更新

对方行原地修改后，不去同步改所有链到它的行：

- 读到链接时比较存下的 `revision` 与对方当前 revision，不一致即摘要过期；
- 过期时照常返回（摘要只是提示，事实以回表为准），并把「刷新摘要」放进修复队列，
  与废弃行的懒更新共用一个队列；
- 刷新是普通的原地修改，照常写 history。

## 不做的

- **不设链接类型**：没有 `relation_type`、`description`，链接只有对方 RowID、摘要和 revision；
- **不设上限**：链接条数与摘要长度都不限；
- **history 不特殊处理**：加链接、删链接、刷新摘要都是对数据行的原地修改，照常记。

## 引擎前提

链接与摘要不设上限。行正文是 SQLite TEXT，大行由 SQLite 自己存，
本项目不实现 Overflow Page。
