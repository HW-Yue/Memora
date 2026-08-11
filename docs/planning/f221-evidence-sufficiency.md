# F221：Evidence 充分性与导航终止条件

状态：候选，2026-08-11 提出；尚未 Review、尚未获得实现授权。
[执行计划](./execution-plan.md)的第 1 项，是 [F220](./f220-query-working-set.md) 的硬前置。

## 唯一主要结果

Query Agent 的导航终止由**充分性**决定，不再由「出现过任意一条成功 SELECT」决定。
零行或不相关的 SELECT 不终止导航；导航在模型显式声明完成、或预算耗尽时才结束。

一个主要结果。不含多轮记忆（那是 F220），不含评分（那是 F219）。

## 当前缺陷

`internal/agent/query_agent.go`：

```go
if len(result.Evidence) > 0 {
    choice = ProviderToolChoiceNone   // 强制模型立即作答
}
```

而 `selectEvidence()`（同文件 `:346`）只要求：

```go
statement.Statement == "SELECT" && statement.Status == succeeded && statement.Error == nil
```

**不检查行数。** 一条返回零行的 SELECT 同样进入 `result.Evidence`，同样立刻把
`ToolChoice` 锁成 `None`，逼模型凭空作答。默认预算
（`MaxProviderCalls: 4`、`MaxToolCalls: 3`）在此约束下形同虚设。

## 协议边界

### Evidence 分级

`SelectEvidence` 增加判定字段，不改变已有 wire 字段语义：

- `rows > 0` → `substantive`：可支撑答案；
- `rows == 0` 且语句成功 → `empty`：是**导航信号**（此路不通），不是答案证据；
- 语句失败 → 既有错误路径，不变。

`empty` 证据必须保留并进入 Trace（它是 F220 负向记忆的来源），
但不计入「已获得可支撑答案的证据」。

### 终止条件

导航结束当且仅当满足其一，按优先级：

1. 模型在**存在至少一条 `substantive` 证据**时显式返回终态（`FinishStop`、无 tool call、
   非空 content）；
2. `MaxToolCalls` 或 `MaxProviderCalls` 耗尽；
3. 上游错误或取消。

`ToolChoiceNone` 只在情形 1 已经发生后用于收尾那一次调用，
**不再由「Evidence 非空」自动触发**。

### 无证据作答

预算耗尽且没有任何 `substantive` 证据时，**不允许**返回自由文本答案。
必须返回既有的 `ErrQueryMissingSelectEvidence`，并在 Trace 记录稳定 reason code。
禁止让模型在无证据情况下作答，这是防止「答对但证据错误」的硬边界。

### 预算

默认值随终止条件放宽而调整为 `MaxProviderCalls: 8`、`MaxToolCalls: 6`；
其余预算字段不变。上限校验（`validQueryBudget`）同步放宽，
但仍必须是有界的显式常量。

## 明确不做

- 不引入多轮上下文累积（F220）；
- 不改 `protocol/msql` wire、不改 Provider port、不改 Trace 信封版本
  （只增加 Trace 事件字段，向后兼容）；
- 不改 Bootstrap Frame 与其预算；
- 不让模型自述命中替代真实 SELECT 行数判定。

## 改动范围

- `internal/agent/query_agent.go`：`selectEvidence`、主循环终止逻辑、默认预算；
- `internal/agent/query_agent_trace.go`：`empty` 证据与 reason code 的 Trace 记录；
- `internal/agent/query_prompt_test.go` / `query_agent_bounds_test.go`：预期值更新。

不触碰 `internal/store`、`internal/msql`、`protocol/`。

## RED 与完成门

- RED 先证明：一条返回零行的 SELECT 当前会终止导航并迫使模型作答；
- 零行 SELECT 后，模型仍能继续发起工具调用，直至充分或预算耗尽；
- 一条 `substantive` 证据后，模型可以选择继续导航（不被强制收尾），
  也可以显式终态收尾；
- 预算耗尽且零 `substantive` 证据 → `ErrQueryMissingSelectEvidence`，
  不返回自由文本；
- `empty` 证据进入 Trace 且不计入可支撑证据计数；
- 既有 F181 golden 中因终止条件变化而改变的用例逐条更新，
  不得通过放宽断言掩盖行为变化；
- 目标测试、`-race`、Agent import allowlist 与完整 CI 全绿。

## 关联

- [执行计划](./execution-plan.md)
- [已知风险](../development/known-risks.md) 第 2 条
- [F220 Query Working Set](./f220-query-working-set.md)
- [F181 只读 benchmark Query Agent](./f181-read-only-query-agent.md)
