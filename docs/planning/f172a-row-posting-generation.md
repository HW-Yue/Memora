# F172a：Row Posting Generation

规划状态：已批准；2026-08-03 单项 Review 通过，可进入 RED → GREEN → REFACTOR。

## 拆分理由

原 F172 同时包含 generation 持久格式升级和在线 Row publication，两者有独立恢复协议与故障矩阵。
现拆为：F172a 交付 row-only fulltext generation parity；F172b 再接 INSERT/UPDATE/DELETE/supersede。

## 唯一主要结果

任何新建或从旧格式升级后激活的 Page generation 都包含第四棵 Fulltext Tree，其初始内容与
native body 中每个当前 Row 的 F170 document 完全一致。

F172a 不修改在线 `PublishRows`，不索引 Catalog/Route，不增加 MSQL，也不开放查询入口。

## Row document projection

新增一个与存储无关的确定性投影：`catalog.Table + row.Row → fulltext.Document`。

- object identity 使用 Database/Table/Row ID，revision 使用 Row revision；
- schema revision 使用 Row schema version，必须与 Table 相同；
- live Row 的非 NULL Column value 全部进入 fields，field ID 固定为 column_id；
- INTEGER、BOOLEAN、TIMESTAMP、TEXT、RELATION_ID 使用 F170 对应 value kind；
- deleted/superseded Row 生成零 field tombstone；全 NULL live Row 合法且产生零 posting；
- 未知 Column、类型漂移、scope/revision/state 错误稳定拒绝，不能字符串化凑数。

投影不读 Store、不分 chunk、不生成摘要，不依赖 Agent。

## Migration Plan v2

Plan v2 在既有 Catalog、current locator、version locator 之外携带按 RowID 排序的当前 Row body。
Plan digest 覆盖这些 body；校验必须证明 body 与 current locator、Catalog schema、source inventory 一致。

仅保存最新 Row body，History 仍只由 version locator 表达。Plan 是迁移时确定性输入，不成为新的正文权威。

## Generation v2

目录名继续保留 `page-index-v1*` 以兼容 authority marker 与 COW generation identity；manifest 升级为
`memora.page-index-generation/v2`，Plan 升级为 `memora.page-index-migration-plan/v2`。

第四棵树固定为：

```text
kind: fulltext
space_id: MEMFTX
page_file: fulltext.pages
wal_directory: fulltext.wal
```

staging build 使用 F171 `Bootstrap`，随后 flush/sync、严格 reopen，并与 F170 reference postings 对拍。
content digest、manifest digest 和 source reverify 覆盖第四棵树；任一失败不发布 staging。

## v1 兼容升级

- reader 继续只读接受合法 generation v1（三棵树），不把缺失 Fulltext 当作损坏；
- Authority 遇到 marker 指向 v1 时，先按旧树恢复并校验，再通过现有 COW replacement 构建 v2；
- 新 v2 marker durable 发布并 live-open 后才关闭旧 generation；失败时旧 marker 保持有效；
- 升级重试按 plan digest/epoch 收敛，不原地修改 v1 文件；
- 新创建的 Database 直接生成 v2，不经历兼容路径。

兼容读取只为升级，不允许 F172b 在三树 generation 上发布 Row posting。

## Failure matrix

| 证据 | 故障点 | 稳定结果 |
| --- | --- | --- |
| projection table | 五类值、NULL、inactive、类型漂移 | canonical document 或稳定拒绝 |
| Plan mutation | body/locator/schema/digest 不一致 | `ErrInvalid` |
| staging phase | Fulltext build/flush/manifest/source reverify | 无可见半 generation |
| strict reopen | Fulltext Page/WAL/manifest bit flip | `ErrTargetCorrupt` |
| v1 upgrade | build/rename/marker 各阶段故障 | 旧 marker 有效或 outcome unknown 后重试收敛 |
| reference | 大量 current Row、含 tombstone | v2 postings 与 F170 完全一致 |
| race | 升级期间现有读者 | 只读旧完整 generation 或新完整 generation |

F171 已证明 posting store 自身的 split/reopen/codec；F172a 证明它被 generation 协议正确组合，不重写算法。

## RED 与完成门

RED 入口：

```text
go test ./internal/rowfulltext ./internal/pagestoremigration
```

完成时执行投影表、Plan/generation golden、v1 compatibility fixture、staging/replacement fault suite、
受影响包 race 与 `./scripts/ci.sh`。

## 永久边界

- Fulltext Tree 是可重建派生索引；Row body 仍是唯一正文；
- Agent 不访问 Plan、generation 或 store；
- F172a 没有产品查询入口，因此不能宣称关键词检索已可用；
- Catalog/Route documents 留给 F173a，在线 Row 发布留给 F172b。

用户执行授权：2026-08-03 用户要求顺序完成后续 Feature；本 Review 只批准 F172a 上述范围。

开工前结论：PASS。

## 关联

- [F171](./f171-persistent-posting-store.md)
- [Generation v2](../storage/page-index-generation-v2.md)
- [COW replacement](../storage/cow-generation-replacement-v1.md)
- [TDD 协议](./feature-tdd-protocol.md)
