# F132 Schema Migration Execution 开工门

状态：已完成（2026-08-01）。

## 单一主要结果

已批准的 `memora.schema-change-plan/v1` 在 native authority 内重验并原子提交，返回
可核验的 `memora.schema-change-receipt/v1`；失败不留下部分 Schema。

## 产品门

- MSQL 只接受 `APPLY SCHEMA CHANGE PLAN :plan FOR TABLE <scope>`；
- approval 必须以 `APPLY_SCHEMA_CHANGE` 精确绑定 plan hash；
- `blocked`、篡改、越权或 stale plan 在任何写入前拒绝；
- 执行窗口重验 Database/Table/全部 Column guard；做过兼容扫描的计划还要重验完整
  live Row ID/revision 集合，新增、删除或更新 Row 都会使计划失效；
- 一个计划只产生一次 Catalog generation 与 Change Log envelope，daemon reopen 后一致；
- receipt 报告提交序列、目标 revision 与 post-commit verification；
- 可逆变化返回反向 proposal，补偿仍须重新 PLAN、检查 live Row 并再次审批；含 DROP
  的不可逆计划不伪造自动补偿。

## RED 清单

1. Engine 尚不能验证 Schema plan 的 canonical identity/action coverage；
2. Parser/AST/Executor 不认识 APPLY Schema 语句；
3. 无 approval、错误 hash/action、blocked/tampered plan 仍可能到达写路径；
4. planning 后 Catalog 或 scanned Row 集合变化仍能执行；
5. ADD/RENAME/ALTER/DROP 不能在一个原子 Catalog commit 内物化；
6. commit fault 留下部分 Column/revision/Change Log；
7. receipt 无法证明结果，或补偿绕过重新规划；
8. native daemon 执行后 reopen 结果丢失，legacy backend 未明确拒绝。

## 明确不做

- 不提供任意 conversion/backfill expression；F131 的 `blocked` 计划仍不可执行；
- 不物理清除被 DROP Column 的历史 Row value；
- 不把 receipt 当作可直接执行的 undo token；
- 不改变普通 Catalog DDL 的兼容接口。

## 完成门

上述 RED 全部转绿；targeted race、全仓 unit/integration/e2e、双架构构建和 daemon
reopen 旅程通过后，才标记 PASS 并进入 F133。

## 完成证据

- canonical validator 覆盖 blocked/tampered plan、action/impact/guard/hash 与补偿边界；
- native Catalog/Row 重验覆盖 approval、stale scanned Row、完整 action materialization；
- publish fault 证明 Catalog 与 Change Log 零写，DROP 形成 DELETE entry；
- MSQL parser/executor、scope authorization 与 legacy unsupported 边界全绿；
- native daemon PLAN→APPLY→DESCRIBE→reopen 旅程验证目标 revision 持久；
- Canonical Skill quick validation、Codex/Claude adapter 确定性生成；
- targeted race、全仓 unit/integration/e2e 与双架构构建门全绿。

结论：PASS。下一项：F133 Host Input Capture。
