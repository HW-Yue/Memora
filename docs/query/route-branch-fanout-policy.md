# Route Branch Fan-out 策略

状态：**部分被取代**。「不提供产品默认值」与「允许带理由的受控超限」两条已由
[F223](../planning/f223-route-branch-fanout-limit.md)取代：现在有启动默认值 12，
超限一律失败，没有例外通道。本文其余判断仍然有效。

## 问题

Branch 的子节点数量影响 Agent 一次选择的准确率和上下文成本，但数量达到目标值时，
引擎不能机械发明新的语义分组。结构上限也不能与 `SHOW ROUTES` 的单页读取预算混为
同一个配置。

## 仍然有效的结论

每个 Database 使用自己的目标 fan-out `N`，保存为版本化、可发现的语义路由策略
（配置键 `route_policy` 的 `branch_fanout`）。`N` 由负责初始建模的 Agent 根据该库的
领域边界、节点可区分度、预期规模和所用模型能力决定。

当父节点已有 `N` 个 live child，Agent 准备增加一个新索引节点时，必须在两条路径中
显式选择：

1. **语义重构**：检查新节点能否归入已有 Branch，或者能否通过创建、拆分、合并、
   移动 Branch 形成更清楚的分组；
2. **修订本库的 `N`**：若当前语义边界确实需要更多并列分组，用
   `ALTER CONFIGURATION ROUTE_POLICY SET BRANCH_FANOUT :n` 提高上限，并留下
   expected revision、actor 和 reason。

引擎校验 revision、授权、审计和原子性，但不替 Agent 猜节点应该怎样分组，也不替它
在这两条路径之间选择。

引擎仍保留与语义无关、不可由 Agent 突破的资源安全极限（`branch_fanout` 取值
`2..100`），用于防止把树配成不可用形状。资源极限不是 Branch 的语义拆分阈值，
不能作为 Agent 的建模指导。

Leaf 不能成为其他 Route 节点的 parent。若两个不同 Row 对应的 Leaf 需要形成共同语义
组，应创建 Branch 并把两个 Leaf 移入其中，而不是把一个 Leaf 放到另一个 Leaf 下，也
不是把两个不同 Row 的 Leaf 合并成一个 Leaf。

## 已被 F223 取代的部分

- ~~不提供统一的产品默认数值~~ → 启动默认值 12，写入 `route_policy` 的 revision 1；
- ~~允许这一层临时增加 child，并记录 actor、reason、来源事件~~ → 没有例外通道，
  超限一律失败；要继续增加只能显式提高本库的 `N`；
- ~~没有固定的 `N+1` 语义硬上限~~ → 第 `N+1` 个 child 一定失败，三条写入路径一致。

理由：可以带理由绕过的上限，在实现里等价于没有上限。把「继续加」显式化为一次配置
修订，比逐次记录例外更可审计，也让 Agent 的判断只有两个明确选项。

## 与当前实现的差异（已消除）

此前 12 是三处独立常量：Semantic Health、Route Mutation Plan 各写死一份，而普通
`CREATE ROUTE` 完全不检查。F223 之后三者统一读取本库 `branch_fanout`。

`query_budgets.route_children` 仍然只约束一次 `SHOW ROUTES` 的返回预算，与
`branch_fanout` 是两个独立配置键、两条独立 revision 链。

## 待确认

- Agent 建库时依据哪些最小输入决定是否偏离默认的 12，以及怎样回读验证；
- 同一 Database 内 root 与普通 Branch 是否需要各自的 `N`（当前共用一个）；
- 哪些证据足以证明「提高上限」比「重构分组」更合理；
- benchmark 如何为 Agent 提供逐层选择准确率、树深、LLM 调用次数和维护成本证据，
  而不是替 Agent 输出一个全局固定答案。

## 关联

- [F223：Route Branch Fan-out 硬上限](../planning/f223-route-branch-fanout-limit.md)
- [语义 Router](./semantic-routing.md)
- [Route Mutation Plan v1](./route-mutation-plan-v1.md)
- [Semantic Health v2](../agent/semantic-health-v2.md)
- [AI-native 可演化配置](../product/adaptive-configuration.md)
