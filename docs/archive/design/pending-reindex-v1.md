# Pending Reindex v1

状态：F24 已冻结 Row 语义索引失效与 durable reindex queue。

## 触发与原子性

UPDATE/RESTORE 修改 Row 后，若 `index_terms` 或 `route_leaf_ids` 缺失，引擎在同一事务中：

1. 提交新 Row revision、History 与机械索引；
2. 立即把缺失通道的旧 Agent posting/Router membership 置为不可见；
3. 按 `database_id + table_id + row_id` 写入新 revision 的 durable task。

提供其中一个完整快照时只排队另一个通道；两个都提供时清除旧 task。DELETE 清空语义/机械索引并清除 task。Batch rollback 同时恢复 Row、索引和队列。

## Task 状态

Task 保存目标 revision、`need_agent`、`need_router`、pending/leased/failed/completed 状态、attempts、lease owner/token/until、最后错误和更新时间。SQLite 原型重启后状态必须保留。

worker 使用有期限 lease。过期 lease 可被其他 worker 接管；旧 token、旧 task revision 或与当前 Row 不同的 revision 都返回 revision conflict，不能写入索引。失败保留可见错误，显式 retry 回到 pending 并增加下一次 claim 的 attempt。

## 完成协议

完成结果必须为 task 要求的完整词项和/或多叶 membership 快照。引擎在一个 Row transaction 中验证：

- task lease 与 revision 仍有效；
- Row 仍为 live 且 revision 完全一致；
- 词项预算和 Router leaf 约束成立。

验证通过后，同 revision 的 invalid Agent snapshot 可被激活，Router membership 完整替换，task 标记 completed。相同 claim 的完成重放幂等成功；不同内容不能绕过 lease/revision guard。

查询等待期间仍可使用当前 Row、SQL 精确读取和自动机械索引；不得返回旧语义 locator。

## 关联

- [Agent Inverted Index v1（历史）](./agent-index-v1.md)
- [Router Tree v1（历史）](./router-tree-v1.md)
- [MSQL Mutation Executor v1](../../query/msql-mutation.md)
