# F139 Package Upgrade 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 唯一主要结果

已安装只读 Database 可以先生成绑定当前 authority 与候选 package 的确定性升级计划，再经
L2 + plan-hash approval 原子替换为新版本，保持原 Database 身份和只读状态。

## RED

- Plan 不写 Store，绑定当前/目标 snapshot、package hash、Schema revision 与 signer。
- 非只读库、不同 Database ID/name、相同 snapshot、Schema 回退均拒绝。
- Apply 重算 plan；stale current、替换 package 或 approval mismatch 零写入拒绝。
- Catalog、Rows、History、Relation 和索引在一个事务中替换；fault/reopen 不见混合版本。

## 非目标

不合并本地写入；安装库默认只读。可写 fork 的三方合并属于 F142。

## 完成证据

计划只读、approval、stale replay、原子 authority 替换及 race 通过；全仓 CI 全绿。下一项 F140。
