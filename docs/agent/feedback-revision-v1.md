# 反馈、修订与逻辑 Undo v1

状态：F38 实现规格，已冻结。

> **目标形态已改。** 本文提到的 Route membership 是一种独立的挂载关系记录；
> [写入形态](../product/write-model.md)取代了它——**叶子直接挂 RowID**，
> 挂载不再是单独的对象。逻辑 Undo 快照仍需携带同等信息，字段承载方式改变。
> 本文仍如实描述**当前代码**，在实现改完之前可以照它读代码。
> 迁移设计见[叶子直挂 RowID](../storage/leaf-rowid-v1.md)。

## 反馈不修改事实

宿主通过 `memora feedback --event <JSON>` 发送
`memora.feedback-event/v1`。kind 只允许 useful、irrelevant、stale、wrong 或
incomplete；事件绑定 Database、Table、Row ID、已展示 revision、actor 和短
reason。Memora 返回并持久化 `memora.feedback-receipt/v1`，状态为 recorded，
但不执行 MSQL、不修改 Row/History/索引，也不把反馈标签当作事实字段。

相同 event ID 同内容重放收据；不同内容冲突。反馈引用的 Row/revision 必须在
记录时存在，防止对未展示或已变化对象创建含糊候选。

## 显式确认

useful/irrelevant 可以只保留为质量信号。stale、wrong、incomplete 若要修改，
宿主必须重新 SELECT 当前 Row，并通过 `memora.feedback-confirmation/v1` 绑定：

- 原 feedback event 和被展示 revision；
- 新的用户确认 source event；
- 当前 expected revision；
- 正常 `memora.mutation-plan/v1`，或逻辑 undo 请求。

修订计划继续受授权范围、preflight、expected revision、短事务和 verify Policy
保护。确认 source 与 feedback event 必须不同；计划 provenance 使用确认事件，
History reason 必须引用反馈 ID。revision conflict 时重新展示，不能盲重试。

## 逻辑 Undo

Undo 使用已有参数化 `RESTORE database.table ROW :row TO REVISION :target`，并
显式携带 expected schema/current revision、actor、confirmation source、reason、
完整 index terms 和 Route memberships。目标必须是同一 Row 的既有 revision。

RESTORE 新增 `COMPENSATE` revision；它不降低 current revision，不删除或覆盖
History，也不回滚其他 Row。删除后的 Row 可以恢复为 live，live Row 也可以
补偿到历史 tombstone。结果返回新 revision/commit sequence 和被恢复的目标
revision，随后必须验证当前逻辑 Row。

## 收据与边界

`memora.feedback-confirmation-receipt/v1` 返回 confirmed、ignored 或
committed_unverified，包含 feedback/confirmation ID、动作、目标 Row、新
revision/commit sequence 和 warning。未经确认、陈旧 revision、越权、跨 Row
undo 或高风险多 Row 修改都在写入前拒绝。

## 关联

- [质量模型与验收](../product/quality-model.md)
- [MSQL History v1](../query/msql-history.md)
- [Skill 写入流程 v1](./skill-write-v1.md)
