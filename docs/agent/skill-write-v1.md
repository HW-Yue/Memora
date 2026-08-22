# Skill 写入流程 v1

状态：F31 已冻结 Mutation Plan、Policy、短事务和收据协议；F76 已把
SPLIT/MERGE 收敛为单条公开原子 MSQL。

> **目标形态已改。** 本文提到的 Route membership 是一种独立的挂载关系记录；
> [写入形态](../product/write-model.md)取代了它——**叶子直接挂 RowID**，
> 挂载不再是单独的对象。策略里「最多 32 个 Route membership」「同一 Row 仍可属于多个 Leaf」**都保留**。
> 本文仍如实描述**当前代码**，在实现改完之前可以照它读代码。
> 迁移设计见[叶子直挂 RowID](../storage/leaf-rowid-v1.md)。

## Mutation Plan

宿主查询相邻 Row 后，把语义决定写成 `memora.mutation-plan/v1`：

```text
scope + actor + source_event_id + reason
+ authorized_databases
+ preflight[] + steps[] + verify[]
```

Plan 的 decision 为 IGNORE、INSERT、REVISE、MERGE、SPLIT、MOVE 或 RELATE。
IGNORE 没有写步骤；INSERT/REVISE/MOVE/RELATE 各有一个；MERGE/SPLIT 各使用
一条原子 `MERGE ... ROWS ...` / `SPLIT ... ROW ...`，不再由 Skill 拼接普通
UPDATE/DELETE/INSERT。
SUPERSEDE 在 v1 由具体 Schema 下的 REVISE/RELATE 表达，不新增物理动作。

每个 Plan 至少有一条带明确 Row 预期的只读 preflight。非 IGNORE Plan 还
必须有 verify。宿主通过：

```text
memora mutate --plan '<strict JSON>'
```

提交完整 Plan；不得把七种决策退化成无条件 INSERT。

## Policy

CLI 在任何 Tool 调用前严格解码并校验：

- Plan 版本、ID、Database/Table、actor、source event 和 reason；
- 目标 Database 位于 `authorized_databases`；
- MSQL 动作与 decision 形状一致，Table 使用 Database 限定名；
- 最多 8 个步骤；普通单 Row 步骤 `max_affected_rows = 1`，reshape 预算等于
  有界来源与目标总数；
- UPDATE/DELETE 带 expected schema/revision；
- INSERT/UPDATE 带非 nil 的完整 Route membership 快照；
- Route memberships 最多 32 个，稳定 ID 非空且去重。
- 每个目标 Leaf 最多一个活跃 Row；INSERT 必须先确认 Leaf 为空，UPDATE 可以保留
  当前 Row 的 membership，同一 Row 仍可属于多个 Leaf。

显式空数组表示提交空快照；字段缺失不等于空快照。语义冲突、高风险或
越权仍由 Skill 请求用户，不能用扩展 `authorized_databases` 绕过。

## 执行与原子性

执行顺序固定为：

```text
preflight query → one mutation batch → verify query → receipt
```

单步使用 autocommit；MERGE/SPLIT 语句自身由原生 coordinator 原子提交，不再
外包一层无法跨 authority 保证的 `BEGIN ... COMMIT`。
daemon 的统一 BatchSession 再执行 Parser、guard、revision 和影响行数校验。
Row、History、物理索引、Route membership 和 Change Log 继续由 Row transaction
原子更新，没有 Skill 专用 Store 旁路。

## Mutation Receipt

`memora.mutation-receipt/v1` 最多 2,000 字符，包含 Plan ID、decision、状态、
每个逻辑操作的宿主 target、引擎实际返回的 Row/relation object ID、revision/
commit sequence、ignored 数、verify 状态和 warnings。状态为：

- `ignored`：preflight 后确认不写；
- `committed`：事务和 verify 都成功；
- `committed_unverified`：事务已提交，但后续验证失败，调用方必须明确提示。

事务失败不返回伪成功收据。多步变化必须报告同一 commit sequence。

## 关联

- [数据库 Mutation Agent](./database-mutation-agent.md)
- [MSQL Mutation Executor v1](../query/msql-mutation.md)
- [Canonical Skill v1](./canonical-skill-v1.md)
