# F199：短事务 Reconciliation 与 Source Receipt

状态：实现中；2026-08-05 规格已冻结。

## 唯一主要结果

把完整 coverage、F196 claim batch 和 F198 accepted artifact 组装成正式 F195 proposal，经用户对
plan SHA-256 的一次性批准后只通过 MSQL 提交；随后用 `SHOW ASSIMILATION RECEIPT` 对账实际
Row/Relation ID、revision 和 commit sequence。提交结果不确定时只读收据，不盲重放写请求。

## 输入门

- coverage 必须 `complete`，document digest 一致且 `covered_nodes=total_nodes>0`；
- batch 按 extent sequence 从 1 连续排列；空 batch 代表已读窗口没有持久语义，不要求 reviewer；
- 每个非空 batch 必须恰有一个 F198 accepted artifact，batch/extent/document/Database/Job/author
  及每个 claim digest 均匹配；拒绝、漏项或多余 artifact 在任何 MSQL 前失败；
- 所有 claim 使用同一 author、目标 Database、source locator/content digest，语句总数不超过正式
  assimilation 上限；候选 MSQL 和参数只进入 proposal/短事务，不进入 Source Receipt。

## MSQL-only 编排

`AssimilationReconciler` 生产代码只依赖 `protocol/msql` 和注入的 `MSQLExecutor`：

1. `Prepare` 规范排序输入并执行一次
   `REVIEW ASSIMILATION FOR DATABASE <db> USING :proposal`，严格读取返回 plan；
2. 调用方显示 plan 并提供绑定其 SHA-256 的显式用户批准；
3. `SubmitApproved` 执行一次
   `SUBMIT ASSIMILATION PLAN :plan FOR DATABASE <db>`；无论提交返回 committed、in_doubt、协议错误
   或 transport error，都不由该方法再次提交；
4. 安全恢复入口 `Reconcile` 只执行
   `SHOW ASSIMILATION RECEIPT :receipt IN DATABASE <db>`；同进程再次 `SubmitApproved` 自动降级为
   只读 Reconcile。

数据库访问不新增 Agent 私有 Go API、Store 或旁路 Controller。F195 Processor 仍拥有 reservation、
事务和收据持久化。

## Source Receipt 扩展

F195 `AssimilationReceipt` 增加可选的 v1 reviewed evidence：author、有序 reviewer/artifact digest、
coverage revision/count。旧无 evidence 收据仍可读；F199 Reconciler 只接受含完整 evidence 的收据。

每条 statement receipt 另保存执行结果返回的有序 `object_ids`：Row mutation 取 `row_id`，关系 mutation
取 `relation_id`。committed F199 收据要求 object 数量等于 affected rows，并要求 revision 与
commit sequence 存在。收据仍不得包含 MSQL、参数、字段正文、原始窗口、prompt 或 reviewer 推理。

## 不确定状态

- 已知事务失败且 F195 未留下 reservation：返回明确失败，调用方可基于新 guard 重新 Prepare；
- 写请求可能已派发、外层响应丢失或 F195 返回 `in_doubt`：只允许 SHOW；
- SHOW 为 committed 时核对 plan/evidence/statement/object/revision/sequence 后返回；
- SHOW 仍为 in_doubt 时保持该状态，不推测提交成功，也不自动构造第二个 plan。

## TDD 与完成门

RED 先锁定：coverage/review 完整门、Prepare/Submit/SHOW 三类精确 MSQL、hash-bound approval、真实
object ID 收据、提交响应与 SHOW 对拍、transport uncertainty 后只读恢复、同进程重试不再写、
receipt reopen/replay/corruption、无正文持久字节、并发单 dispatch、Agent import guard、race、vet
与全量 CI。

## 关联

- [F195 正式 MSQL 吸收面](./f195-msql-assimilation-surface.md)
- [F198 独立复核门](./f198-independent-review-gate.md)
- [资料吸收 Agent Feature 序列](./assimilation-agent-feature-sequence.md)
