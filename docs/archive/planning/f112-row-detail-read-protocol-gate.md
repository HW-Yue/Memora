# F112 Row Detail Read Protocol 开工与完成门

状态：PASS；2026-08-01 完成。

## 唯一主要结果

按 RowID 的 SELECT 与该 Row 的 History 返回 Data Dictionary 驱动、可分页续读的稳定
result envelope，未来 Admin 无需猜业务列。

## RED

- point SELECT 没有 Table/Schema/展示角色 envelope；
- Result Column metadata 没有稳定 Column ID、purpose、role 和 ordinal；
- Column DDL 无法声明展示角色，重复 title/summary 没有确定性拒绝；
- SHOW HISTORY 只有 `truncated`，没有 cursor/snapshot/version，第二页无法读取；
- cursor 被篡改、跨 Row 使用或历史在两页间变化时没有稳定失败。

## 完成门

- Parser/Binder/Catalog/原生 codec 的 role round-trip 与 corruption/兼容证据；
- legacy/native point SELECT 都返回动态字段和相同 row-detail 契约；
- legacy/native History 多页不重不漏，cursor scope、tamper、snapshot conflict 全覆盖；
- authorization 仍先于 Row/History 输出，History 不泄露 values；
- reopen、真实 daemon envelope、race 与完整 `scripts/ci.sh` 全绿；
- 独立提交并快进合入 `main`。

## 明确不做

- committed change timeline、Route trace 或页面；
- 根据列名猜展示角色；
- HNSW、vector 或其他检索后端。

## 完成证据

- RED：Parser 拒绝 `ROLE/CURSOR`，point SELECT 缺少 `RowDetail`，History 第二页不可读；
- GREEN：legacy/native SELECT 与 History、严格 cursor unit、role codec/validation 全绿；
- snapshot：tamper、跨 Row、mutation continuation conflict 均返回稳定 code；
- durability：native Catalog role 与 History cursor 跨 reopen 保持等价；
- boundary：History 固定 metadata columns，不含 values，Row 正文只由 SELECT 返回；
- real path：daemon 验证 role 驱动 Row detail 与两页 History envelope；
- `scripts/ci.sh`：format、vet、unit、full race、integration、e2e、cross-build 全绿。
