# Logical Snapshot v1

状态：F26a/F26b 已冻结版本化逻辑格式、v0 迁移、确定性哈希、Store 导出、原子导入和逻辑索引重建。

> **目标形态已改。** 本文的信封按"History 是一种系统对象"导出归属，
> 已被[写入形态](../product/write-model.md)取代：history 独立成表，
> 且**binlog 是唯一恢复依据**——备份与恢复都只依赖 binlog，不再依赖快照 + Change Log。
> 逻辑快照在新形态下的定位（是否保留、导出什么）待定。
> 本文仍如实描述**当前代码**，在实现改完之前可以照它读代码，
> 但**不能作为新开发的设计依据**。

## 迁移边界

Logical Snapshot 是 SQLite 原型迁往 `.memora` 原生底座的安全出口，不复制数据库
文件、SQLite schema、rowid、WAL 或任一派生索引。v1 信封只保存：

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

Row 定位、History revision、Relation 正反向定位、Agent、Router、机械倒排及 generation manifest 都不属于快照权威内容。导入在同一 Store transaction 中重建前三类逻辑定位并恢复 commit sequence；语义和机械索引保持可丢弃、可由 Row 与 Catalog 重建。

导入只接受空目标，先完成全量格式与引用验证，再原子写入。失败或 rollback 不留下部分 Catalog、Row、History、Relation 或定位索引。

## 关联

- [Catalog v1](../data/catalog-v1.md)
- [Row Store v1](../data/row-store-v1.md)
- [History Store v1](../data/history-store-v1.md)
- [Relationship Store v1](../data/relationship-store-v1.md)
- [原生极简 Store ADR](../decisions/0003-native-minimal-store-first.md)
