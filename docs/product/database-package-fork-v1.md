# Database Package Fork v1

状态：F141 已实现。

## 计划与授权

`memora.package-fork-plan/v1` 绑定已安装只读源库的 Database ID、package/snapshot hash，以及
调用方选定的全新目标 Database ID/name。Plan 只读并返回 review hash；Apply 重算计划，需要
源与目标的 L2 scope，以及 action `FORK_PACKAGE_DATABASE` 的 plan-hash approval。

## 身份派生

fork 复制完整当前 Row、History 和库内 Relation，但不是第二个同身份安装：

- Database 使用调用方给出的新 ID/name；
- Table、Column 与 Relation ID 由目标 Database ID + 原 ID 确定性派生；
- Column-ID keyed Row/History values 和 Relation endpoints 同步 remap；
- Row ID、revision、Schema version、commit sequence 和语义文本保持；
- 新库清除 package install provenance，记录 fork base Database/package/snapshot hash。

变换后的逻辑 snapshot 先完整 `prepare` 校验，再通过单事务 MergeImport 发布。新库
`read_only=false`，源库仍为只读且字节不变。目标冲突、stale source 或 plan 改写均零写入失败。

fork 后的本地变化与上游新包如何比较和合并由 F142 定义。
