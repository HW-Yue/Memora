# 走树的宽度预算：按父节点计，溢出如实报 incomplete

状态：**实现计划**（未开工，等开工）。实测见 `docs/decisions.md` 的两条记录。

## 问题（可复现）

`undecided`（jev 答"不确定"）意味着整层展开；但 BFS 每层末把**所有分支展开出的孩子并成一个数组**，
按到达顺序（语句顺序 = 表的列举顺序）只留 `MAX_FRONTIER = 4`，其余丢弃，而且**丢掉的照样写进
`landings`**，只靠 `termination: "budget: frontier width"` 区分。

- 两库两表：深度 0 得 4 + 5 = 9 → 留前 4 → `memora.modules` 整张表的根分支全丢，答案
  `/当前状态/当前缺口` 够不到，而输出里 21 个 landings 看着很丰满。
- 指定 `table=modules`：一张表、根层 5 个分支 → 仍留 4 丢 1 → 恰好丢排第 5 的 `/运行与宿主`。
  **答案的生死由列举顺序决定。**
- 引擎自己 `route_policy.branch_fanout = 12`，比脚本的 4 宽。
- 已排除："把并集交给 jev 挑"——实测无判别力（九值 .47–.66、地板 .37），rekey 那问把 .83 给了
  无关的 `工作方式`，真正的两个分支排第 4、第 5。

## 修法（一个 Feature，一个分支）

1. **宽度预算按父节点计**（根因）：待展开的父节点逐个展开、各用各的名额，不让一个分支的 `undecided`
   吃掉另一个分支的名额。
2. **溢出不展开、不产 landings**：该层如实标 `incomplete`，带上被截断的候选数与触发阈值。
3. **上限对齐 `branch_fanout = 12`**：当成常量对齐，不新增配置项、不发明第二套预算配置。

## 判据

- RED 先写那个可复现的两库两表用例：断言 `modules` 的根分支不被丢、`landings` 里不含未展开的节点。
- 溢出时 `incomplete` 为真，且输出里没有假落点。

## 坑

- `incomplete` 是新的输出状态：Skill 文案与 Agent 提示词要知道怎么读它，否则模型看到 `incomplete`
  照样当完整答案用。
- 按父节点分预算会让总 frontier 变宽（9 个父 × 12），需要一个全局刹车；刹车触发时同样报
  `incomplete`，**不能退回悄悄截断**。
- 两个分支都改 `skills/memora/`：本块必须从 `route-purpose-contract` 合回后的主线切，
  否则 `scripts/sync-skill.sh` 的四份副本会打架。
