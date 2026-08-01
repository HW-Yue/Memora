# F131 Schema Change Plan 开工与完成门

状态：已完成（2026-08-01）。

## 单一主要结果

AI 通过只读 MSQL 提交显式 Column/constraint proposal；引擎对当前 Table Schema 与
必要的 live Row 做有界兼容性检查，返回绑定 snapshot/hash 的可审阅迁移计划。

## 产品门

- MSQL 固定绑定一个 Table，proposal 不携带可扩大权限的 scope；
- 支持 ADD、RENAME、ALTER、DROP Column 的显式意图，不由引擎猜目标 Schema；
- 全量 Column surface、Database/Table revision 与必要 RowID/revision 进入 guard；
- 类型/长度/NULL 收紧必须逐 Row 验证，扫描截断时拒绝生成“可执行”计划；
- 不兼容变化返回 `blocked` 与有界 blocker，不伪装成可执行计划；
- F131 只读，不执行 DDL、Row rewrite、补偿或 approval。

## RED 清单

1. Parser/AST/Executor 不认识 `PLAN SCHEMA CHANGE ... USING :proposal`；
2. 同一 Column 重复 action、stale revision、名称/alias/role 冲突仍能出计划；
3. ADD NOT NULL、类型变化、TEXT 收窄不检查 live Row；
4. Row scan 截断仍返回 `review_required`；
5. input/Column 顺序变化导致 plan/hash 漂移；
6. 计划生成修改 Catalog/Row，或能被 F131 直接执行；
7. legacy/native daemon、read-only allowlist 与 Canonical Skill 漂移。

## 明确不做

- 不执行或补偿计划；
- 不提供任意 conversion expression 或自动生成 Row 值；
- 不跨 Table 移动 Column，不删除物理 Row/History 值；
- 不把现有 host `memora.schema-plan/v1` 当作新的引擎计划协议。

## 完成证据

- reference model 覆盖 deterministic shuffle、stale/duplicate、name/alias/role 冲突；
- ADD NOT NULL、ALTER constraint、DROP impact 的 Row scan、blocker 与 truncation 门全绿；
- Parser/AST/Executor、read-only CLI policy 与 legacy/native daemon no-write 旅程全绿；
- Canonical Skill quick validation 与 Codex/Claude adapter 确定性生成；
- targeted race、全仓 unit/integration/e2e 与双架构构建门全绿。

结论：PASS。下一项：F132 Schema Migration Execution。
