# Database Package Merge v1

状态：F142 已实现。

## 三方输入

Merge 明确接收 fork selector、fork provenance 精确绑定的 base package，以及同源的新 upstream
package。base snapshot hash 不匹配、upstream Database 身份不同、Schema 回退或 upstream 已撤销
都会在计划前拒绝。引擎不依赖当前安装源库仍保留旧 base，因此计划可离线重验。

base 与 upstream 先使用 F141 相同规则映射到 fork 身份，再与当前 fork snapshot 对 Schema、Row
和 Relation 逐对象比较：

- 本地未改、upstream 已改：采用 upstream；
- upstream 未改、本地已改：保留本地；
- 两侧结果相同：采用该结果；
- 两侧结果不同：稳定列入 `SCHEMA`、`ROW:<table>/<row>` 或 `RELATION:<id>` conflict。

History 跟随最终 Row/Relation 来源，不能拼接出不存在的 revision 链。

## 计划与提交

`memora.package-merge-plan/v1` 绑定 base/local/upstream/merged 四个 snapshot/package hash。
conflict 计划不可 Apply，语义选择留给 AI/用户后续普通修订。无冲突计划需 fork Database 的 L2
Authorization 和 `MERGE_PACKAGE_FORK` plan-hash approval；Apply 完整重算三方结果，再以单事务
替换 fork authority 与逻辑索引。fork 保持可写，merge base 推进到 incoming snapshot。
