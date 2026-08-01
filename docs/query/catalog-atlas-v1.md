# Catalog Atlas v1

状态：F135 已完成（2026-08-01）。

## 目标

Catalog Atlas 用一条有界 MSQL 同时暴露授权 Database 与 Table 的短语义描述，减少
“先选库再逐库查表”的模型续推，同时保留完整分页覆盖：

```sql
SHOW CATALOG ATLAS CURSOR :cursor LIMIT :limit BYTES :bytes COMPACT;
```

## 扁平条目

稳定顺序是 Database 名称/ID，随后是该库的 Table 名称/ID。Database 条目含 kind、
database ID/name/aliases、purpose、scope、anti-scope、schema version；Table 条目额外含
table ID/name、purpose、scope、anti-scope、row semantics、schema version。

Atlas 不展开 Column，不返回 Route、RowID、正文或事实。扁平形态避免在每张 Table
重复嵌套完整 Database 对象，也保证空 Database 仍有一个可见条目。

## 双预算、授权与 cursor

- `LIMIT` 约束条目数，范围 1–256，默认 64；
- `BYTES` 约束返回 rows JSON 的 UTF-8 bytes，范围 512–65,536，默认 16,384；
- 单条 metadata 都装不进 BYTES 时返回 `output_truncated`，不突破预算；
- authorization 在读取各库 Table 前过滤，未授权对象不进入 rows 或 snapshot；
- page 返回 snapshot、truncated 和 next cursor；cursor 绑定 Atlas scope、授权后的完整
  snapshot 和 offset，篡改、跨列表复用或 Catalog drift 均失败。

`truncated=false` 才证明该 snapshot 的授权 Catalog coverage complete。预测器无命中
不改变 coverage；冷库通过 next cursor 确定性可达。

## 关联

- [MSQL Catalog DDL v1](./catalog-ddl.md)
- [Speculative Discovery Skill v1](../agent/speculative-discovery-skill-v1.md)
- [Catalog Navigation v1](../development/catalog-navigation-v1.md)
