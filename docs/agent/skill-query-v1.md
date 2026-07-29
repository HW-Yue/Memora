# Skill 查询流程 v1

状态：F30 已冻结首个有界宿主查询状态机。

## 输入与职责

宿主语义层把用户问题整理为 Database、Table、可选绝对 Route path、
1–32 个去重 `query_terms`、投影字段和预算。状态机不调用模型生成答案，
只编排 MSQL 并收集可用于回答的 evidence。

```text
SHOW DATABASES
→ DESCRIBE TABLE ... COMPACT
→ 可选 OPEN ROUTE
→ MATCH（无 Route、Route 空或 not_found 时）
→ 校验 locator
→ SELECT projected fields, row_id, revision
```

Route/MATCH 行只能包含定位和分数，永远不能进入 evidence。只有 SELECT
成功返回、Row ID 相同且 revision 与 locator 一致的逻辑 Row，才允许宿主
总结。Evidence 同时保留 Database/Table 名称与稳定 ID、Row ID、revision
和选出的字段，用于来源定位。

## 预算与停止

Canonical Skill v1 约束候选最多 24 条、SELECT 最多 10 条。状态机还要求：

- Database、Table 和投影字段必须显式提供；
- query terms 非空、大小写不敏感去重；
- 标识符统一引用，值只通过参数传递；
- 重复 locator 只 SELECT 一次；
- 跨 Database/Table、缺 ID 或 revision 的 locator 丢弃；
- 候选超过回表预算时设置 `truncated`。

有足够 SELECT evidence、候选耗尽、硬预算到达、权限拒绝或后续调用已不能
改变结果时停止。`AnswerReady` 只在至少存在一条 evidence 时为真。

## 可恢复终态

- `complete`：所有使用中的候选已回表且无告警；
- `partial`：有 evidence，但候选或 SELECT 被截断、失效或部分拒绝；
- `no_results`：Database/Table/候选不存在，或候选回表后全部失效；
- `access_denied`：权限阻止发现或全部回表。

`not_found`、`output_truncated` 和 `permission_denied` 转成可见终态或 warning。
其他 Parser、协议或内部错误保留稳定 code 返回，不能伪装成“没有资料”。
Route 权限拒绝不会通过 MATCH 绕过。

## 关联

- [Canonical Skill v1](./canonical-skill-v1.md)
- [Index Discovery v1](../query/index-discovery-v1.md)
- [MATCH Fusion v1](../query/match-fusion-v1.md)
- [Scripted Host Harness v1](../development/scripted-host-harness-v1.md)
