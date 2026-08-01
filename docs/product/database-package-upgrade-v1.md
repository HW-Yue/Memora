# Database Package Upgrade v1

状态：F139 已实现。

## 计划

`PlanUpgrade` 只接受与已安装只读库相同 Database ID 和名称的候选包。当前安装 provenance
必须完整，候选 snapshot 必须不同，且 Schema version 不得回退。计划固定绑定：

- 当前与候选 package/snapshot SHA-256；
- Database ID、名称和两侧 Schema version；
- 候选 verified signer key ID（若存在）；
- `memora.package-upgrade-plan/v1` 自身 hash。

计划为 `review_required` 且不写 Store。Apply 需要该库 L2 Authorization，以及 action 为
`APPLY_PACKAGE_UPGRADE`、subject 为 plan hash 的 approval；执行前完整重算计划。

## 原子替换

Apply 在一个 Store transaction 中移除旧库的 Catalog、Rows、History、库内 Relations 与各逻辑
索引，再写入候选 authority 和新的安装 provenance。Database ID/名称保持不变，结果仍为只读。
stale current、package 替换、签名差异或 approval mismatch 都在事务前拒绝；重放已提交计划因
当前 snapshot 已变化而失败。

本协议适合不可本地修改的安装库。可写 fork 不能走覆盖升级，必须使用 F142 merge。
