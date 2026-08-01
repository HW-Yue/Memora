# MSQL Committed Change Read v1

状态：F113 已完成并验收；2026-08-01 冻结 v1 语法、分页与授权边界。

## 目的

Admin 与 Agent 只通过 MSQL 读取 F109 的 committed change envelope。时间线按独立
change commit sequence 排序；Page index 只定位 immutable envelope，不复制 Row 正文。

## Timeline

```sql
SHOW CHANGES [IN DATABASE work]
  [AFTER COMMIT_SEQUENCE :sequence]
  [CURSOR :cursor]
  LIMIT :limit;
```

- `LIMIT` 必填，范围 1–256；AFTER 缺省为 0；
- AFTER 与 CURSOR 互斥；cursor 是首屏后唯一 continuation；
- 每行是一笔完整事务的 summary：version、transaction ID、commit sequence、时间、
  actor/source/reason、可选 receipt、可见 database scope、entry count 与 checksum；
- summary 不包含 entries、Row values、prompt、PageID 或模型推理。
- `IN DATABASE` 的 summary 只返回所选稳定 Database ID，并把 `entry_count` 收敛为该
  Database 的可见 entry 数；跨库事务的其他 scope 不进入单库 Admin 响应。

首屏固定 Page index 当前 high-water 为 snapshot。后续新 commit 不改变旧 snapshot；cursor
绑定全局/Database scope、snapshot high-water 和已消费 commit sequence，因此分页不重、
不漏，也不会混合两个快照。

## Transaction entries

```sql
SHOW CHANGE :transaction_id [IN DATABASE work] [CURSOR :cursor] LIMIT :limit;
```

结果按 envelope 内 canonical entry 顺序分页。每行只返回稳定 object scope、operation、
before/after revision、Schema version、History locator 和 related object IDs。完整 Row
内容仍按 locator/RowID 使用 SELECT/History 读取。
空 `related_object_ids` 确定编码为 `[]`，与非 nullable `ID_LIST` column contract 一致。

entry cursor 绑定 transaction checksum、scope 和 offset。损坏、跨事务或跨 Database
使用返回 `validation_error`；envelope checksum 不一致视为 corruption，不返回半笔事务。

## 授权

带 Database scope 的查询先绑定当前稳定 Database ID，再过滤 summary/entry。带有限
Database authorization 的调用必须显式使用 `IN DATABASE`；不能用全局时间线枚举其他
Database。无 authorization 的本地管理会话可读取全 Instance。

## 边界

- 产品读取只走 derived Page change index，禁止回退到 `nativechange.ListAfter`；
- immutable envelope body 仍是真相源，Page index 可由它在 open/read reconcile；
- 不做 retention、同步、PITR、页面或正文 diff；
- Route trace 属于 F114，不写入 committed change timeline。

## 关联

- [Committed Change Envelope v1](../storage/committed-change-envelope-v1.md)
- [Committed Change Page Index v1](../storage/committed-change-page-index-v1.md)
- [F113 开工与完成门](../archive/planning/f113-change-read-protocol-gate.md)
