# Logical Snapshot v1

状态：F26a 已冻结版本化逻辑格式、v0 迁移与确定性哈希；F26b 接入 Store 导出、原子导入和逻辑索引重建。

## 迁移边界

Logical Snapshot 是 SQLite 原型迁往原生内核的安全出口，不复制数据库文件、SQLite schema、rowid、WAL 或任一派生索引。v1 信封只保存：

```text
version = memora.logical-snapshot/v1
catalog
rows[]
history[]
relations.current[]
relations.versions[]
commit_sequence
```

Catalog、Row、History 和 Relation record 继续使用各自的稳定逻辑 ID、revision、commit sequence 和版本信封。数组中的 record 保留原始 JSON object，因此新 writer 增加而旧 reader 不理解的字段可以穿过迁移，不被静默删除。

## 兼容规则

- v1 reader 明确拒绝未知 snapshot 主版本；
- `memora.logical-snapshot/v0` fixture 可迁移到 v1；
- v0 的 flat `relations[]` 转为 current 与 version-1 历史；
- 未识别的顶层字段、Catalog 字段和 record 字段必须保留；
- identity 重复、悬空 Row/History/Relation、非连续 relation revision 和倒退的 commit sequence 必须拒绝；
- 兼容失败使用稳定错误码，不依赖自然语言匹配。

`CanonicalHash` 先迁移并验证，再对 JSON 语义做确定性编码和 SHA-256；空白、object key 顺序不影响结果。

## 索引边界

Row 定位、History revision、Relation 正反向定位、Agent、Router、机械倒排及 generation manifest 都不属于快照权威内容。F26b 导入时重建前三类逻辑定位；语义和机械索引保持可丢弃、可由 Row 与 Catalog 重建。

## 关联

- [Catalog v1](../data/catalog-v1.md)
- [Row Store v1](../data/row-store-v1.md)
- [History Store v1](../data/history-store-v1.md)
- [Relationship Store v1](../data/relationship-store-v1.md)
- [SQLite 原型 Store ADR](../decisions/0001-prototype-store.md)
