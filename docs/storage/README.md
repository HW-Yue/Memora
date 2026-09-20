# 存储层：当前形态

状态：**现状描述**（2026-09-20）。本层现在没有自研实现，因此本文很短，
且不再有下级 Feature 规格。历史的自研引擎设计全部在
[`../archive/storage/`](../archive/storage/)，只用于追溯，不是设计依据。

## 一句话

Memora 不实现存储引擎。持久化基座是 **SQLite**，
一切产品结构都是普通 SQLite 表（[ADR-0011](../decisions/0011-pure-storage-engine-tables-everything.md)）。
词法倒排与向量索引的**当前内核已删除**，召回架构待规划，见
[查询形态](../product/query-model.md)。

## 事实

代码在 `internal/sqlstore`。`internal/sqlstore/db.go` 开头的注释是
这一层的权威描述：

```text
Everything Memora keeps is an ordinary SQLite table (ADR-0011): the Catalog,
every data table, its history table, its semantic-route table, the change
log, and configuration. There is no MVCC of Memora's own: writers are
serialised, readers see the last commit. Keyword/vector recall is not in
this kernel; that architecture is planned separately.
```

- 一个实例 = 一个 SQLite 文件（`<dataDir>/databases/<file>`，
  由 `internal/daemon/lifecycle.go` 打开）；
- **不做 MVCC**：写事务串行，读只看最后一次提交。没有快照、read view 或 undo；
- Page、Buffer Pool、WAL、B+ Tree、页格式、校验和、崩溃恢复**由 SQLite 承担**，
  不是本项目的实现对象，也不在本项目测试范围；
- 现役表：Catalog、数据表、history、语义配套、`mem_changes`、配置、轨迹、KV。
  旧库里可能还留着 `mem_postings` / `mem_vectors`，新 schema 不再创建它们。

## 为什么没有自研引擎了

ADR-0003 到 ADR-0006 曾把自研 Page/Buffer Pool/WAL/COW/B+ Tree 定为方向，
`docs/storage/` 下曾有 63 份对应规格，代码约两万行。ADR-0011 把边界收缩为
「存储引擎只做数据库，其余一切建表」，实施时进一步裁定直接采用 SQLite：
产品价值在语义层，不在重造一个已被验证了二十年的存储引擎。

那批设计与完成证据保留在 [`../archive/storage/`](../archive/storage/)，
对应的 ADR 在 `decisions/` 中标记为 Superseded。
