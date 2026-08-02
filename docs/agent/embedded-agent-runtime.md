# 可选内置 Agent Runtime

状态：F43 已决定面向用户的 v0 Runtime defer；F175c/F176 已交付评测 Agent 所需的 MSQL-only
边界和无模型 Bootstrap，但 Provider、loop 与产品入口仍需后续独立 Feature。

## 定位

v0 由 Codex/Claude Code 按 Canonical Skill 生成 MSQL，并调用本地 daemon。面向用户的
`memora ask` 仍按 [ADR-0002](../decisions/0002-defer-embedded-agent.md) 延后；标准化静态评测
可以先评估一个隔离的内置 benchmark Agent driver。后者的角色和外置 Hook 边界见
[内置评测 Agent 与外置 Hook 观测](../development/evaluation-agent-observability.md)，不能据此
推定完整产品 Runtime 已获实现授权。

内置 Agent 负责语义决策，引擎仍负责确定性执行。二者可以随同一个本地产品发布，但必须保持模块边界。

## 两种入口，一个执行核心

```text
自然语言
→ memora ask
  → Agent Loop
  → MSQL Request ─┐
                  ├→ Lexer/Parser → Binder → Policy → Planner
外部 Agent / CLI  │                    → Transaction → Executor
  → memora exec   │                    → JSON Envelope
  → MSQL Request ─┘
```

- `memora ask` 接收自然语言，由内置 loop 发现 Schema、生成 MSQL、读取结构化结果并继续推理；
- `memora exec`、SDK 和可选 MCP adapter 直接提交 MSQL；
- 两条路径最终调用同一个版本化 `ExecuteMSQL` 接口；
- 内置 Agent 不能直接构造绕过 Parser 的内部写操作，也不能访问 Page、索引或日志文件。

“统一入口”指统一进入 MSQL 执行核心，不把自然语言并入 MSQL Grammar。自然语言始终先由 Agent 转换为明确的 MSQL。

## 硬依赖边界

内置 Agent 模块只能通过一个由调用方注入的版本化 `MSQLExecutor` 端口访问 Memora：

```text
internal/agent/* → MSQL Request/Result Port → Lexer/Parser/Binder/Policy/Transaction/Executor
```

Agent 可以拥有 Provider、Source Reader、Document IR、Session、Checkpoint、Event 和 Trace 等运行时
组件，但它们是 Agent-owned operational state，不是访问用户 Database 的替代通道。Agent 包禁止
直接依赖 Catalog、Row、Router、Relation、History、Assimilation Controller、Store、Page、WAL、
MVCC 和任何索引实现；同进程部署也不能放宽该规则。

若 Agent 需要的发现、资料覆盖、复核、提交、收据或管理能力尚未由 MSQL 表达，开发顺序必须是
先冻结并实现 MSQL，再让 Agent 使用。现有 `assimilation.*` 私有 IPC 不属于未来 Agent 合法工具面。
CI 必须检查 Agent package import allowlist，并用 fake `MSQLExecutor` 证明 Agent 测试不打开 Instance、
不读取数据库文件且不调用引擎 Go API。

## 统一请求信封

内部 loop 与外部调用方统一使用 `protocol/msql.Request`：顶层包含 `version`、完整 MSQL `source`
和逐 statement 输入；每项输入携带 parameters、mutation budgets/provenance 与显式 Authorization。
返回值统一为 `protocol/msql.Envelope`。actor、权限、预算和审批状态可以不同，但语法解析、AST、
类型检查、Policy 和事务语义不能分叉；旧讨论稿中的自然语言 `mode/budget/scope` 信封已被该协议取代。

## Agent Loop

```text
接收意图
→ 恢复紧凑 Query Workspace
→ 让模型读取当前 Route Frame 并选择下一条 MSQL
→ 通过 ExecuteMSQL 执行
→ 读取稳定 JSON 结果或错误
→ 继续、修正、请求批准或结束
→ 返回回答 / Context Pack / Mutation Receipt
→ 保存可重建的 checkpoint
```

循环必须有确定性的最大步数、token、时间、返回字符、扫描行数和修改行数。达到预算时显式返回 `truncated` 或 `budget_exhausted`，不能无限自治。

查询采用 Bootstrap + Route/SQL 两段分工。第一次模型调用前，Runtime 只通过 MSQL
取得完整有界 Catalog Atlas、全内容 lexical locations，并可投机预取最多两张高可能性 Table 的根 Route；模型在第一次调用中一次选择多个 Table，并选择已预取 Route、要求
展开其他 Route 或按 lexical RowID 发起 `SELECT`。之后只保留所选 Schema、当前 Route
Frame 与 SQL 事实 Row，不再携带整个 Atlas。发现结果不能直接作为最终答案。

Runtime 校验每层节点数、最大深度、Leaf 单 Row 不变量和跨 Leaf 回表总预算。Data Dictionary
提供已知 alias，MSQL 负责正式传递，Policy 强制权限与预算。Runtime 不生成
`query_terms` 交给评分器，也不执行 MATCH/Vector/cosine 降级。

## 一个 Runtime，多种能力配置

不再要求 Query Agent 和 Mutation Agent 是两个不同宿主进程。一个 loop 根据请求获得能力配置：

- read profile：只允许 SHOW、DESCRIBE、ROUTE、SELECT、EXPLAIN；
- write profile：允许可逆局部修改，但仍受 revision、Schema version 和影响行数约束；
- schema profile：先生成影响计划并等待 Policy 或用户批准；
- destructive profile：PURGE、清历史和放宽隐私必须显式授权。

能力由引擎 Policy 强制，不依赖 prompt 自律。

## 本地 daemon 与上下文

本地 `memora` daemon 长期打开一个 Instance，并在同一进程中承载存储引擎、Agent Runtime、Provider 连接、文件 Page 的 Buffer Pool 和按 scope 隔离的 Query Workspace。CLI、`--stdio` bridge、SDK 和可选 MCP adapter 都是 daemon 的本地客户端，不能各自创建互不一致的缓存与事务域。

直接运行 `memora` 时默认进入前台交互循环，直到用户 `exit`，但数据库和缓存继续由 daemon 持有。外部 Agent 可以通过 `memora --stdio` 使用 JSONL bridge；单次 `ask`/`exec` 命令连接 daemon、执行并退出，不重新打开 Instance。

Buffer Pool 按 Page 的访问热度保留最近读取的数据和索引，Agent 上一次查询访问的 Page 因而通常仍在内存，但不会获得语义优先级。Query Workspace 只保存当前 loop 的有界状态，模型原始上下文不能无限增长；它与物理 Page 缓存分别计费和失效。

## 模型与密钥

- 用户可以配置云模型或本地模型；
- API Key 进入操作系统密钥存储或进程环境，不进入用户数据库、prompt、日志或 Wiki 导出；
- 每个 Database/Policy 可以限制是否允许把内容发送给外部模型；
- Provider 不可用时，`memora exec` 的确定性 MSQL 能力仍应可用。

## 第一版 CLI 形态

```text
memora                       进入交互式 Agent/MSQL 循环
memora --stdio               启动供外部调用方持有的 JSONL 会话
memora ask <intent>          使用内置 Agent Loop
memora exec <statement>      直接执行 MSQL
memora daemon ...            启动、停止和检查本地 daemon
memora config model ...      配置模型，密钥单独安全保存
```

## 若重新开启仍需冻结

- Provider 抽象和第一版支持范围；
- daemon 的启动、退出、崩溃恢复、本地 IPC 和客户端会话协议；
- Query Workspace 的持久化位置、TTL 和最大尺寸；
- loop 的默认步数、token、时间和费用预算；
- 写入审批如何同时支持交互 CLI 和非交互调用；
- 模型输出采用工具调用还是严格 JSON；
- `ask` 最终返回人类回答、Context Pack，还是由调用方选择；
- 同一 loop 是否处理资料吸收，还是使用独立任务类型。
- 现有资料吸收私有 IPC 迁移到 MSQL 的语法、授权、预算和兼容边界。

## 关联

- [MSQL](../query/msql.md)
- [Agent 的 MSQL 边界与依赖注入](./agent-msql-dependency-injection.md)
- [上下文生命周期](../query/context-lifecycle.md)
- [AI 自主权与约束](./autonomy.md)
