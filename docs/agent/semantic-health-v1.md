# 语义数据库健康维护 v1

状态：F37 实现规格，已冻结。

## 确定性报告

`memora maintain --report` 返回 `memora.semantic-health/v1`。报告只包含稳定
locator、计数、风险等级、建议动作和 report SHA-256，不返回 Row 正文。Issue
按 kind、Database、Table 和 object ID 排序，并由同一组字段生成稳定 ID。

v1 检查：

- 同一 Table 内值映射完全相同的 live Row：`duplicate_row`；
- 类型、NULL 约束和规范化 purpose 相同的多个字段：`synonymous_columns`；
- 达到启动容量 12 children 或 100 Rows 的 Router 节点：Router capacity；
- 非 completed 的 durable `pending_reindex`；
- 最新 Row 比 Table 说明更新时间晚至少 30 天：`stale_description`。

这些是维护候选，不是语义事实。扫描超过有界 Row 预算时报告 truncated，不能
把未发现问题解释成健康。

## 风险与动作

重复 Row、同义字段、Router split/merge 和说明重写均为 `review_required`。
引擎不能自动删除/合并 Row、改 Schema、发明 Router 分支或重写描述；Skill
必须 SELECT 回表，必要时请求用户，再走既有 Mutation/Schema Policy。

只有状态为 failed 的 pending_reindex retry 是 `low_risk` 且 `auto_fix=true`，
因为它只恢复可重建派生索引任务，不修改 Row、History 或语义决定。pending、
leased 和 completed task 不自动改状态。

## 维护请求与收据

`memora maintain --request <JSON>` 接收 `memora.maintenance-request/v1`：稳定
request ID、checkpoint 或 user_request 触发、actor、expected report hash 和
显式 issue ID。执行前重新生成报告；hash 变化返回 revision conflict。

请求只能选择当前报告中 `auto_fix=true` 的 issue。成功返回
`memora.maintenance-receipt/v1`，逐项记录 retry target 和状态；相同 request ID
同内容重放收据，不同内容冲突。空选择返回 noop。高风险 issue 即使由宿主放入
请求也在任何写入前拒绝。

Canonical Skill 只在用户明确要求或显式 conversation checkpoint 时运行报告；
不得假设隐藏 lifecycle hook，也不得把每轮对话都变成维护扫描。

## 关联

- [Pending Reindex v1](../data/pending-reindex-v1.md)
- [Router Tree v1](../query/router-tree-v1.md)
- [Skill 写入流程 v1](./skill-write-v1.md)
- [Skill Schema 生命周期 v1](./skill-schema-lifecycle-v1.md)
