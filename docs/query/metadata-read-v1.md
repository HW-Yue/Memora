# MSQL Metadata Read v1

状态：F110 已实现并冻结。

## 目的

Admin 和 Agent 只通过 MSQL 读取 Database、Table 与 Column 元数据，不读取 Store、
Page 或 Catalog 内部结构。所有 Catalog 列表都有硬上限和可验证 continuation，不能一次
无界返回整个 Catalog。

## 语法

```sql
SHOW DATABASES [CURSOR :cursor] [LIMIT :limit] [COMPACT];
SHOW TABLES FROM work [CURSOR :cursor] [LIMIT :limit] [COMPACT];
SHOW COLUMNS FROM work.notes [CURSOR :cursor] [LIMIT :limit] [COMPACT];
```

`LIMIT` 缺省为 64，最大为 256；必须是正整数 literal 或 parameter。`CURSOR` 必须是
string literal 或 parameter。显式语句推荐用于 Admin，缺省值只保留旧调用兼容性。

`DESCRIBE DATABASE/TABLE/COLUMN ... COMPACT` 是有界 point read。Admin 展开下一层时
必须改用对应 `SHOW ... LIMIT`，不能依赖非 `COMPACT` 的嵌套 Schema 结果分页。

## List Page envelope

Catalog 列表的 statement result 额外返回：

```json
{
  "page": {
    "version": "memora.list-page/v1",
    "limit": 16,
    "cursor": "",
    "snapshot": "sha256:...",
    "truncated": true,
    "next_cursor": "..."
  }
}
```

`cursor` 是本次请求收到的不透明 cursor，首屏为空；`next_cursor` 只在还有下一页时
出现。statement 既有的 `truncated/next_cursor` 与 `page` 必须一致，以保留 result v1
客户端兼容性。

## Snapshot 与 cursor

- Snapshot 是调用方当前授权范围内、当前列表 scope 的确定性 SHA-256 标识；
- cursor 绑定协议版本、scope、snapshot 和下一 offset，并带完整性校验；
- cursor 损坏、跨 scope 使用或超预算一律 `validation_error`；
- continuation 时 Catalog 已变化则返回 `revision_conflict`，不混合两个快照；
- cursor 不是授权凭据、不能扩大 database authorization，也不能长期持久化。

v1 不保存 Catalog 历史 read view，因此选择 fail-fast continuation。未来若 Catalog 获得
MVCC snapshot，可保持本协议外形而替换内部 cursor 实现。

## 边界

- v1 在 MSQL 边界限制返回量；Catalog/Page 内部的原生 range scan 优化不是本 Feature；
- 不包含 Route、Row detail、Change 或 Trace，它们分别由 F111–F114 冻结；
- 不增加 Admin 专用 Store API，也不返回 PageID、offset 或 B+ Tree key。

## 关联

- [Catalog DDL v1](./catalog-ddl.md)
- [MSQL Result Envelope v1](./result-envelope.md)
- [F110 开工与完成门](../planning/f110-metadata-read-protocol-gate.md)
