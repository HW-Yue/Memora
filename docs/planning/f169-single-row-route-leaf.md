# F169：Route Leaf 单 Row 不变量

状态：已实现；2026-08-02 用户将 Leaf 一对多认定为重大产品缺陷并授权修复。

> **目标形态已改——但不变量保留。** 本文冻结的"一个 Leaf 最多一个活跃 Row"
> 被[写入形态](../product/write-model.md) §4.4 原样保留，仍然有效。
> 变的是它靠什么实现：本文经独立的 **Membership** 关系维护并校验这条不变量，
> 新形态下**叶子直接挂 RowID**，不变量成为叶子自身的结构性质。
> 本文仍如实描述**当前代码**，在实现改完之前可以照它读代码，
> 但**不能作为新开发的设计依据**。

## 产品门

- 目标用户故事：`US-READ`、`US-INSERT`、`US-UPDATE`、`US-SPLIT`、`US-DBA`；
- 用户结果：AI 选中终端语义 Leaf 后只得到一个确定 Row locator，不再读取候选桶中的
  多条 Row 才能判断；
- 唯一主要结果：每个 Route Leaf 最多挂载一个活跃 Row；
- 保留能力：同一 Row 可以属于多个 Leaf，不复制正文；
- 明确不做：不改变物理 Page 格式，不让引擎生成语义名称，不把 Row 正文或向量加入
  Route，不删除 `OPEN ROUTE` 的兼容语法。

## 标准 MSQL 旅程

```sql
SHOW ROUTES FROM TABLE project_memora.knowledge AT ROOT LIMIT 12;
SHOW ROUTES UNDER :branch LIMIT 12;
OPEN ROUTE :leaf LIMIT 1;
SELECT * FROM project_memora.knowledge WHERE row_id = :row LIMIT 1;
```

`OPEN ROUTE` 的结果基数只能是 0 或 1。Branch 负责继续分层；当一个语义范围包含多个
Row 时，AI 必须创建更多 Branch/Leaf，直到每个终端 Leaf 确定性定位一个 Row。

## 不变量与失败语义

- `Leaf → active Row` 的基数是 `0..1`；
- `Row → active Leaf` 的基数仍是 `0..N`；
- INSERT、UPDATE、SPLIT、MERGE、Route membership move 都按事务最终状态验证；
- 原子 move 可以在同一事务中释放旧 occupant 并写入新 occupant；
- 第二个活跃 Row 进入已占用 Leaf 时返回稳定 `constraint_violation`，Row、History、
  membership 与 Change Log 全部不发布；
- 读取到历史遗留的一对多 Leaf 时，`OPEN ROUTE` 返回约束错误，不把错误结构伪装成
  合法候选页；Semantic Health 仍可报告 Leaf 与 RowID；
- 历史坏 Leaf 只允许 occupant 数严格减少的单调修复事务，不能新增或维持歧义；语义
  名称与树形重塑必须由 AI DBA 完成。

## 上下文预算

每层仍只返回有限 Route child；Leaf 只返回一个 locator，随后只回表一条 Row。Branch
达到本 Database 的目标 fan-out 后由 Agent 决定重构、例外增加或修订目标值，不能通过
扩大 Leaf locator 页吸收规模；后续策略见
[Route Branch Fan-out](../query/route-branch-fanout-policy.md)。

## RED 与验收

- RED：第二条 Row 插入同一 Leaf 必须失败且第一条 locator 保持可读；
- 保留：一条 Row 同时写入两个 Leaf 必须成功；
- 原子性：批处理、事务回滚、split/merge 和 Route move 不能留下双 occupant；
- reopen：重启后单 Row locator 与约束保持一致；
- legacy：人工构造一对多旧记录时 `OPEN ROUTE` 稳定拒绝；
- 真实旅程：把 `project_memora` 的四个旧 Leaf 改为 Branch，并为 15 条知识建立一对一
  Leaf，验证 Route → locator → SQL 回表。

## 完成证据

- RED：MSQL、原生 Repository 与兼容 Router 均先证明第二个 Row 可进入同一 Leaf；
- GREEN：所有普通写入、reshape 和 Route Plan 在提交前校验最终 membership 状态；
- legacy：普通 `OPEN` 拒绝多 Row Leaf，维护扫描仍能报告并允许单调减少修复；
- 原子性：覆盖失败零部分状态、同事务 Leaf 转移、并发竞争 one-winner、split/merge、
  rollback 和 reopen；
- 门测：`go test ./...`、`go test -tags=integration ./...`、
  `go test -tags=e2e ./...`、`go test -race ./...`、`go vet ./...` 全部通过；
- 真实实例：`project_memora.knowledge` 已从 4 个多 Row Leaf 迁为 4 个 Branch 与 15 个
  单 Row Leaf；15 次 `OPEN ROUTE ... LIMIT 1` 均返回一条且不截断；
- 真实拒绝：向已占用 Leaf 插入第二个探针 Row 返回 `constraint_violation`，回查零行，
  `memora doctor` 仍为 `healthy`；持久 `OPEN_LOCATORS` 已从 24 修订为 1。

完成结论：PASS。
