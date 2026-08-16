# F223：Route Branch Fan-out 硬上限

状态：已实现（2026-08-16）。取代 [Route Branch Fan-out 策略](../query/route-branch-fanout-policy.md)
中「不设产品默认值、允许带理由的受控超限」的方向。

## 问题

一个语义索引节点下面挂多少个子节点，直接决定 Agent 单层选择的准确率。当前实现里：

- `nativerouter.prepareChild` **完全不检查子节点数**，`CREATE ROUTE ... CHILD` 可以无限追加；
- `routemutationplan` 把 12 写死成常量，只覆盖 split/merge/move，绕过它即可越界；
- `semantichealth` 在 12 个 child 时报 `route_capacity`，但只是事后报告；
- Admin 前端全量渲染，超限在界面上看不出异常。

结果是「一个节点下面应该挂多少个」既没有权威值，也没有任何写入时的拦截。

## 方向性结论

1. 每个 Database 有一个**结构 fan-out 上限** `branch_fanout`，启动默认值 **12**。
2. 任何会让某个父节点的 live child 数超过上限的写入，**一律失败**，不是警告、不是分页、
   不是带理由的例外。
3. 失败返回结构化信封，明确给出两条互斥出路，由 Agent 自己判断走哪条：
   - **重构子树**：合并、下沉或拆分节点，让新节点归入已有分组；
   - **提高本库上限**：`ALTER CONFIGURATION ROUTE_POLICY SET BRANCH_FANOUT :n`，
     **一次最多加 4**（`router.MaxBranchFanoutIncrease`）。加宽语义树是需要反复判断的决定，
     不是一次性的：每次提高都要单独的理由和单独的 revision，因此拥挤的父节点不能靠
     一步跳到天花板解决。降低上限不受此限制，只有增长需要审议。
     越界错误里会直接给出本次最多能调到的值，Agent 不必试错。
4. 引擎不替 Agent 选。它只保证上限可发现、越界必然失败、两条出路都可执行。
5. `100`（`router.MaxConfigurableBranchFanout`）是配置值的天花板，**不是默认值也不是目标**；
   按每次 +4 计算，从 12 走到 100 需要 22 次各自独立论证的提高。

`branch_fanout` 是**结构**上限，与 `query_budgets.route_children`（一次 `SHOW ROUTES`
的读取分页预算）是两个概念，因此使用独立配置键、独立 revision 链和独立理由。

## 配置面

新增 Database 级配置键 `route_policy`，与 `query_budgets` 并列，同样版本化、可发现、
随 logical snapshot 与 Database Package 迁移：

```sql
SHOW CONFIGURATION ROUTE_POLICY;
SHOW CONFIGURATION ROUTE_POLICY HISTORY LIMIT :limit;
ALTER CONFIGURATION ROUTE_POLICY SET BRANCH_FANOUT :fanout;
RESTORE CONFIGURATION ROUTE_POLICY TO REVISION :revision;
```

- 启动默认 `branch_fanout = 12`；已有 Database 在下次打开时补齐 revision 1；
- 引擎资源上限 `2..100`：这不是语义建议，只防止把树配成不可用形状；
- 修改仍需 expected revision、actor 和 reason，与 `query_budgets` 同一套 Policy；
- 降低上限**不回溯**：既有超限父节点保持可读可查，只是不能再加。

## 强制面

所有会增加某个父节点 live child 的结构写入走同一个检查：

| 路径 | 位置 |
| --- | --- |
| `CREATE ROUTE ... CHILD` | `nativerouter.prepareChild` |
| Route Mutation Plan 的 split / merge / move | `routemutationplan.Build` |
| Plan apply 阶段的 planned create / move | `nativemutation` reshape 提交 |

root 与 branch 共用同一个 `N`。Leaf 不能成为 parent，这条不变量不受本 Feature 影响。

越界错误：`constraint_violation`，`details` 至少包含

```json
{
  "reason": "route_branch_fanout_exceeded",
  "parent_route_id": "route_x",
  "live_children": 12,
  "branch_fanout": 12,
  "remedies": [
    {"kind": "restructure_subtree", "statement": "PLAN ROUTE MUTATION ..."},
    {"kind": "raise_branch_fanout", "statement": "ALTER CONFIGURATION ROUTE_POLICY SET BRANCH_FANOUT :fanout"}
  ]
}
```

## 存量超限

只拦新增。既有超限父节点仍可读、可查、可删子节点、可被重构；`semantichealth` 继续用
本库当前 `branch_fanout`（不再是硬编码 12）报告 `route_capacity`，供 Agent 决定重构还是提高上限。

## RED

1. 一个已有 12 个 live child 的 branch，第 13 次 `CREATE ROUTE ... CHILD` 当前**成功**；
2. `SHOW CONFIGURATION ROUTE_POLICY` 当前是 parse error；
3. `semantichealth` 与 `routemutationplan` 当前用各自的常量 12，改 `branch_fanout` 不影响它们。

## 完成判据

- 三条写入路径在同一上限上失败，错误带上述 `details`；
- `ALTER CONFIGURATION ROUTE_POLICY SET BRANCH_FANOUT 16` 之后第 13～16 个 child 可创建，
  第 17 个失败；
- `route_policy` 进入 logical snapshot 与 Database Package，reopen 后 revision 链完整；
- 存量超限库仍可读、可删、可重构，且不被任何非结构写入阻塞；
- Skill 契约与 `SKILL.md` 描述这两条出路，Agent 无需人类介入即可选择。

## 关联

- [Route Branch Fan-out 策略](../query/route-branch-fanout-policy.md)（被本文取代的部分已就地标注）
- [AI-native 可演化配置](../product/adaptive-configuration.md)
- [语义 Router](../query/semantic-routing.md)
- [Semantic Health v2](../agent/semantic-health-v2.md)
