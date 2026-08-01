# F142 Package Merge 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 唯一主要结果

可写 fork 以其绑定的 base package、本地当前 snapshot 和新 upstream package 做确定性三方比较；
无冲突计划经 L2/hash-bound approval 原子合入 fork，有冲突只报告而不写入。

## RED

- base snapshot 必须等于 fork provenance，upstream 必须同源且未撤销。
- Schema、Row、Relation 逐对象三方比较；单侧变化合并，双方相同变化合并，双方不同变化冲突。
- conflict 计划稳定列出对象且不可 Apply；不得由引擎猜业务语义。
- Apply 重算 base/local/upstream 和 merged hash；stale fork 或换包零写入拒绝。
- Catalog、Rows、History、Relations 与索引单事务替换，fork 保持可写并推进 merge base。

## 完成证据

非重叠合并、双改冲突零写入、base/stale/hash 门与 race 通过；全仓 CI 全绿。下一项 F143。
