# F173b1：Route Posting Generation

规划状态：已完成；2026-08-03 通过完整完成门并获准合入。

## 拆分理由

Route 进入 migration Plan、generation seed 和 reopen reconciliation 会升级持久化快照协议；在线
Route CRUD、Mutation Plan 与 Row+Route reshape 则改变运行时发布协议。两者版本兼容、故障点和
验收旅程独立。本项只完成前者，F173b2 再统一在线发布。

## 唯一主要结果

新 generation 和 Authority reopen 都让当前 live Route semantic surface 与 Fulltext Tree 一致；
旧 row/catalog-only generation 可增量补齐，revision 无法连续时自动 COW。

## 用户故事与标准旅程

- `US-F173B1-1`：新建或重建 Instance 后，所有当前 Route 都已有 lexical postings；
- `US-F173B1-2`：升级旧 generation 时，无需用户手动执行重建；
- `US-F173B1-3`：native truth 已删除 Route 时，重开不会保留陈旧 Route 位置。

本项不新增 SQL。F174 前仍无用户可见 lexical query，内部以 posting/reference 对拍验收。

## Route document

每个当前 live `router.Node` 生成一个完整 `fulltext.KindRoute` document：

- identity：database ID、table ID、route ID；revision 使用 Route revision；
- fields：name、aliases、path、kind、purpose、synopsis；
- aliases 排序，空白 synopsis 不产生 field，其余必填语义文本缺失时拒绝；
- membership、Row、Column value、History、向量和正文不得进入 Route document；
- deleted Node 不进入当前 snapshot；恢复删除时用旧 object scope 与 `revision+1` tombstone。

投影必须确定、无 Store/Agent/模型依赖，并复用 F170 tokenizer/reference contract。

## Plan 与 generation v3

Page migration Plan 升级到 v3，新增按 Route ID 排序的 `current_routes`。native Source 在同一 inventory
fingerprint 前后读取 Catalog、Row versions 和当前 live Routes；Plan 校验 Route scope 必须指向当前
Catalog Table、revision/kind/tree identity 合法，并把 Route snapshot 纳入 digest。

Page generation 升级到 v3，但仍是 Catalog/Current/Versions/Fulltext 四棵树。Fulltext seed 为：

```text
current Catalog documents + current Route documents + current Row documents
```

v1 三树与 v2 四树 manifest 都继续可打开；新 build 只写 v3。旧 generation 不因版本号本身强制 COW。

## Reopen reconciliation

Authority 从 v3 Plan 投影当前 Catalog/Route/Row，并读取 Fulltext object inventory：

- 当前 live Route 缺失且 revision=1：增量插入；
- 当前 revision 与已存 revision 连续：替换；
- active Route object 已从 snapshot 消失：生成一次 tombstone；
- first revision >1、scope drift 或 revision gap：返回 rebuild-required，沿用 COW replacement；
- inactive stale object 不再次推进；结构损坏 fail closed，不借 rebuild 绕过。

## Failure matrix

| 证据 | 故障/边界 | 稳定结果 |
| --- | --- | --- |
| projection | root/branch/leaf、alias、空 synopsis、非法 scope/kind/revision | canonical document 或稳定拒绝 |
| Plan v3 | Route 顺序、重复、Catalog scope、source 前后变化、digest tamper | 确定 Plan 或 `ErrSourceChanged/ErrInvalid` |
| seed | Catalog + Route + Row | generation 与 F170 reference postings 一致 |
| recovery delete | active object 不在 current Routes | tombstone，旧 posting 为零 |
| v2 upgrade | missing Route rev1 | 原 generation 内增量补齐 |
| v2 gap | missing Route rev>1 | COW 新 epoch，旧 generation 保留 |
| corruption | native Route 或 Fulltext mirror 损坏 | fail closed |
| race | source inventory 中途变化 | 不发布混合 snapshot |

## 产品门审计

- 上下文预算增加 0，不新增模型、Provider、Agent、向量、chunk 或查询入口；
- Route posting 是可重建导航位置，native Route 仍是 authority，最终事实仍须 SQL 回表；
- 不改变一个 Leaf 最多一个活跃 Row、Agent 自治 fan-out 或 Route membership；
- 在线 publication、显式 rebuild 与 MSQL location query 分别留给 F173b2、F173c、F174；
- 用户 2026-08-03 的连续执行授权覆盖本拆分项，不扩张相邻 Feature 的主要结果。

RED 入口：`go test ./internal/routefulltext ./internal/pagestoremigration`。

开工前结论：PASS。

## 完成证据

- `routefulltext.Project` 覆盖 name/aliases/path/kind/purpose/synopsis，并稳定拒绝非法身份、状态和 kind；
- Plan v3 将 canonical current Routes 纳入 source binding/digest，校验 Catalog scope、父树、cycle、root 和记录下界；
- generation v3 seed 已与 Catalog/Route/Row reference postings 对拍；
- v2 缺失 Route rev1 原地增量补齐，删除生成 tombstone，first rev2 则 COW 且保留旧 generation；
- native Route record ID、revision gap 和 revision identity 损坏均 fail closed；
- 实现提交 `6e5c45f`；`./scripts/ci.sh` 的 format、vet、unit、race、integration、e2e 全绿，
  cross-build 单独复跑全绿。

完成门结论：PASS。未覆盖在线 Route publication、显式 rebuild 和用户可见 lexical query，分别由
F173b2、F173c、F174 承担。

## 关联

- [F173a](./f173a-catalog-posting-publication.md)
- [Route Vector semantic surface](../query/route-vector-generation-v1.md)
- [TDD 协议](./feature-tdd-protocol.md)
