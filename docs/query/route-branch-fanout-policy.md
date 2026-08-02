# Route Branch Fan-out 策略

状态：讨论稿；方向已确认，公开协议和实现 Feature 待单独评审。

## 问题

Branch 的子节点数量影响 Agent 一次选择的准确率和上下文成本，但数量达到目标值时，
引擎不能机械发明新的语义分组。结构上限也不能与 `SHOW ROUTES` 的单页读取预算混为
同一个配置。

## 方向性结论

每个 Database 使用自己的目标 fan-out `N`。`N` 由负责初始建模的 Agent 根据该库的
领域边界、节点可区分度、预期规模和所用模型能力决定，并作为 Database 的版本化、
可发现语义路由策略保存。Memora 不提供统一的产品默认数值，也不把 12 写成语义常量。

当父节点已有 `N` 个 live child，Agent 准备增加一个新索引节点时，必须先在以下两条
路径中显式选择：

1. **语义重构**：检查新节点能否归入已有 Branch，或者能否通过创建、拆分、合并、
   移动 Branch 形成更清楚的分组；
2. **受控特例**：若当前语义边界不适合重构，允许这一层临时增加 child，并记录
   actor、reason、来源事件和父节点 revision。

超过 `N` 是需要 Agent 明确判断的状态，不自动等于损坏，也没有固定的 `N+1` 语义硬
上限。每次继续增加都必须留下独立理由；Semantic Health 报告目标值、当前值和例外
记录，供 Agent 决定继续保留、重构节点或修订本库的 `N`。引擎校验 revision、授权、
审计和原子性，但不替 Agent 猜节点应该怎样分组或最多允许几次特例。

引擎仍保留与语义无关、不可由 Agent 突破的资源安全极限，用于防止单次请求或损坏数据
耗尽内存和上下文。资源极限不是 Branch 的语义拆分阈值，不能作为 Agent 的建模指导。

Leaf 不能成为其他 Route 节点的 parent。若两个不同 Row 对应的 Leaf 需要形成共同语义
组，应创建 Branch 并把两个 Leaf 移入其中，而不是把一个 Leaf 放到另一个 Leaf 下，也
不是把两个不同 Row 的 Leaf 合并成一个 Leaf。

## 与当前实现的差异

当前实现以 12 为多处独立常量：Semantic Health 在 12 个 child 时报告容量问题，Route
Mutation Plan 拒绝结果超过 12，而原生普通 `CREATE ROUTE` 尚未统一执行相同边界。
这些行为已被本方向取代。后续 Feature 需要迁移为 Database 级 `N`、结构化提示和显式
例外记录，不能继续把 12 当作所有 Database 的语义容量。

`query_budgets.route_children` 当前只约束一次 `SHOW ROUTES` 的返回预算。它不能直接
充当 Database 的 Branch 目标 `N`，因为读取分页大小和树的语义 fan-out 是两个不同
概念。新的 Database 策略字段必须拥有独立名称、revision 和来源理由。

## 待确认

- Agent 建库时依据哪些最小输入生成首个 `N`，以及怎样回读验证；
- 同一 Database 内 root 与普通 Branch 是否默认共享 `N`，何时允许局部例外；
- 每次超目标写入的公开 MSQL/Mutation 信封怎样表达；
- 哪些证据足以证明“暂不重构”比立即分组更合理；
- Agent 何时应该修订 Database 的 `N`，而不是持续追加节点例外；
- benchmark 如何为 Agent 提供逐层选择准确率、树深、LLM 调用次数和维护成本证据，
  而不是替 Agent输出一个全局固定答案。

## 关联

- [语义 Router](./semantic-routing.md)
- [Route Mutation Plan v1](./route-mutation-plan-v1.md)
- [Semantic Health v2](../agent/semantic-health-v2.md)
- [AI-native 可演化配置](../product/adaptive-configuration.md)
