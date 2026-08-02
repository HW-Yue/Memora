# F173a：Catalog Posting Publication

规划状态：已完成；2026-08-03 通过完整完成门并获准合入。

## 拆分理由

Catalog mutation 已经由 `PageAuthority.PublishCatalog` 发布；Route mutation 仍由
`nativemutation.CommitRoutePlan` 直接提交 native transaction，两者端口、锁、恢复 source 和故障点独立。
本项只完成 Catalog；F173b 单独接 Route，F173c 再提供显式全量 rebuild/parity。

## 唯一主要结果

Generation seed、Catalog DDL 和 reopen reconciliation 都让当前 Database/Table/Column semantic surface
与 Fulltext Tree 一致；rename/alias/schema revision 替换旧词项，drop 生成 tombstone 并移除旧词项。

## 用户故事与标准旅程

- `US-F173A-1`：Agent 创建或重命名 Database/Table/Column 后，未来 lexical location 使用最新描述；
- `US-F173A-2`：删除 Column 或写入中断后，重开不会留下旧名称命中；
- `US-F173A-3`：已有 v2 Row-only generation 升级后自动补齐 Catalog，不要求用户重建。

不新增 SQL：

```sql
CREATE DATABASE work PURPOSE 'Work knowledge' SCOPE 'projects';
CREATE TABLE work.notes (title TEXT(200)) PURPOSE 'Notes' ROW SEMANTICS 'one note';
ALTER TABLE work.notes RENAME COLUMN title TO heading;
APPLY SCHEMA CHANGE work.notes PLAN :drop_column_plan;
```

F174 前仍无用户可见 lexical query；本项由内部 posting/reference 证据验收。

## Catalog document

- Database：name、aliases、purpose、scope、anti_scope；identity/revision 使用 database ID/schema version；
- Table：name、aliases、purpose、scope、anti_scope、row_semantics；identity/revision 使用 table ID/schema version；
- Column：name、aliases、type、purpose、semantic_role；identity/revision 使用 column ID/schema version；
- 空白可选文本不产生 field；字段 ID 固定且不保存原始正文副本；
- 当前 snapshot 只生成 live document，drop 用旧 object scope 和 `revision+1` 生成零 field tombstone。

投影必须确定、无 Store/Agent/模型依赖，并先于 body commit 完成。

## Seed、发布与恢复

Generation v2 Fulltext bootstrap 改为 `current Catalog documents + current Row documents`，严格 reopen
继续与 F170 reference postings 对拍。

在线发布在同一 Authority barrier 内执行：

```text
project before/after + tombstones
→ commit immutable Catalog/change bodies
→ replace Catalog authority Tree
→ one Fulltext batch replacement
→ success
```

Catalog Tree 成功、Fulltext 失败时返回 outcome unknown 并 poison；reopen 从 native Catalog 收敛。
为识别已从当前 snapshot 消失的 object，F171 Store 增加只读 object inventory，并完整验证
object/owner/posting mirror。active stale Catalog object 会生成 tombstone；inactive object不重复推进 revision。

若旧 Row-only generation 中 Catalog revision 已大于 1，incremental first revision 会冲突；启动沿用 F172b
COW replacement，以当前 Plan 一次 seed 全部 Catalog/Row documents。结构损坏仍 fail closed，不借 rebuild 绕过。

## Failure matrix

| 证据 | 故障点 | 稳定结果 |
| --- | --- | --- |
| projection | 三类对象、别名、空白、scope/revision drift | canonical document 或稳定拒绝 |
| seed | empty/large Catalog + Row | generation 与 F170 reference postings 一致 |
| rename | Database/Table/Column revision | old term 删除，new/alias term 指向新 revision |
| drop | Column 从 snapshot 消失 | revision+1 tombstone，旧 posting 为零 |
| publication | body/Catalog/Fulltext checkpoint、WAL fault | poison；reopen 收敛 |
| inventory | object/owner/posting corruption | `ErrCorrupt`，无猜测/漏删 |
| upgrade | Row-only v2、revision gap | COW 新 epoch，旧 generation 保留 |
| race | Catalog readers/writer | barrier 外只观察完整前/后状态 |

## 产品门审计

- 上下文预算增加 0；不新增模型、向量、chunk、Provider 或 Agent 端口；
- posting 仍是有界位置派生索引，Catalog Tree/native body 才是语义定义 authority；
- Route、显式 rebuild command、MSQL location query 分别留给 F173b、F173c、F174；
- 用户 2026-08-03 的连续执行授权覆盖本拆分后的 F173a，不扩张到后续项。

RED 入口：`go test ./internal/catalogfulltext ./internal/store/fulltextindex ./internal/pagestoremigration`。

开工前结论：PASS。

## 完成证据

- `Project` 确定性覆盖 Database/Table/Column，空白可选字段跳过，非法层级、revision 和必填语义拒绝；
- generation seed 与 reopen reference 同时包含当前 Catalog 和当前 Row；
- create/rename/drop、三个 publication checkpoint、真实 Fulltext WAL 故障和 poison/reopen 均已验证；
- Fulltext object inventory 会校验 object/owner/posting mirror，损坏时 fail closed；
- Row-only v2 且 Catalog revision gap 的 fixture 自动发布 COW epoch，旧 generation 保留；
- 实现提交 `6701508`；`./scripts/ci.sh` 的 format、vet、unit、race、integration、e2e、cross-build 全绿。

完成门结论：PASS。未覆盖 Route publication、显式 rebuild 和用户可见 lexical query，分别由
F173b、F173c、F174 承担。

## 关联

- [ADR-0008](../decisions/0008-full-content-inverted-index.md)
- [F172b](./f172b-live-row-posting-publication.md)
- [TDD 协议](./feature-tdd-protocol.md)
